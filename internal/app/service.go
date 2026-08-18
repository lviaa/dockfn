package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound       = errors.New("application not found")
	ErrNoRollback     = errors.New("no previous successful package is available")
	ErrTargetOffline  = errors.New("target host port is unavailable")
	ErrRegistration   = errors.New("fnOS registration operation failed")
	ErrUnsafeArtifact = errors.New("generated artifact escaped its staging directory")
)

type Repository interface {
	List(context.Context) ([]AppSpec, error)
	Get(context.Context, string) (AppSpec, error)
	Create(context.Context, AppSpec) error
	Update(context.Context, AppSpec) error
	Delete(context.Context, string) error
	LastError(context.Context, string) (string, error)
	SetLastError(context.Context, string, string) error
}

type BuildSource struct {
	Directory string
}

type Builder interface {
	Render(context.Context, AppSpec) (BuildSource, error)
}

type InstalledArtifact struct {
	FPKPath string
}

type Platform interface {
	Install(context.Context, string, AppSpec, string) (InstalledArtifact, error)
	Remove(context.Context, AppSpec) error
	Installed(context.Context, AppSpec) (bool, error)
}

type Probe func(context.Context, uint16) error

type SettingsReader interface {
	Get(context.Context) (Settings, error)
}

type Service struct {
	Repo         Repository
	Builder      Builder
	Platform     Platform
	Discoverer   Discoverer
	DataDir      string
	StagingDir   string
	Probe        Probe
	Settings     SettingsReader
	NewID        func() (string, error)
	ProbeTimeout time.Duration
	mu           sync.Mutex
}

func (s *Service) Create(ctx context.Context, input Input) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, err := s.id()
	if err != nil {
		return View{}, err
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return View{}, err
	}
	identity, err := ResolveIdentity(id, settings.EntryPrefixTemplate, input.EntryPrefix, input.DisplayName)
	if err != nil {
		return View{}, &ValidationError{Fields: []FieldError{{
			Field: "entryPrefix", Message: err.Error(),
		}}}
	}
	if err = s.ensureAppNameAvailable(ctx, identity.AppName, ""); err != nil {
		return View{}, err
	}
	spec := AppSpec{
		ID:              id,
		AppName:         identity.AppName,
		EntryID:         identity.EntryID,
		DisplayName:     strings.TrimSpace(input.DisplayName),
		Description:     strings.TrimSpace(input.Description),
		ShowDockFNBadge: Bool(settings.ShowDockFNBadge),
		Origin:          normalizeOrigin(input.Origin),
		OpenType:        NormalizeOpenType(firstNonEmpty(input.OpenType, settings.DefaultOpenType)),
		Protocol:        strings.ToLower(strings.TrimSpace(input.Protocol)),
		Port:            input.Port,
		Path:            normalizedInputPath(input.Path),
		AllUsers:        input.AllUsers,
		Revision:        1,
	}
	var createdIcon string
	spec.IconPath, _, err = s.resolveIcon(input, "")
	if err != nil {
		return View{}, err
	}
	createdIcon = spec.IconPath
	if err = Validate(spec); err != nil {
		s.removeOrphanIcon(createdIcon)
		return View{}, err
	}
	if err = s.probe(ctx, spec.Port); err != nil {
		s.removeOrphanIcon(createdIcon)
		return View{}, fmt.Errorf("%w: %v", ErrTargetOffline, err)
	}
	source, err := s.Builder.Render(ctx, spec)
	if err != nil {
		s.removeOrphanIcon(createdIcon)
		return View{}, err
	}
	defer s.cleanupSource(source.Directory)
	artifact, err := s.Platform.Install(ctx, "install", spec, source.Directory)
	if err != nil {
		s.removeOrphanIcon(createdIcon)
		return View{}, fmt.Errorf("%w: %v", ErrRegistration, err)
	}
	if err = s.commitArtifact(spec.ID, artifact.FPKPath, AppSpec{}); err != nil {
		_ = s.Platform.Remove(ctx, spec)
		s.removeOrphanIcon(createdIcon)
		return View{}, err
	}
	if err = s.Repo.Create(ctx, spec); err != nil {
		_ = s.Platform.Remove(ctx, spec)
		s.removeCurrent(spec.ID)
		s.removeOrphanIcon(createdIcon)
		return View{}, err
	}
	_ = s.Repo.SetLastError(ctx, spec.ID, "")
	return s.check(ctx, spec)
}

func (s *Service) Update(ctx context.Context, id string, input Input) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Repo.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	currentEntryID := EffectiveEntryID(current)
	requestedEntryID := strings.TrimSpace(input.EntryPrefix)
	if requestedEntryID == "" {
		requestedEntryID = currentEntryID
	}
	settings, settingsErr := s.settings(ctx)
	if settingsErr != nil {
		return View{}, settingsErr
	}
	var requestedIdentity Identity
	if requestedEntryID != currentEntryID {
		requestedIdentity, err = ResolveIdentity(current.ID, settings.EntryPrefixTemplate, requestedEntryID, input.DisplayName)
		if err != nil {
			return View{}, &ValidationError{Fields: []FieldError{{
				Field: "entryPrefix", Message: err.Error(),
			}}}
		}
		if err = s.ensureAppNameAvailable(ctx, requestedIdentity.AppName, current.ID); err != nil {
			return View{}, err
		}
	}
	next := current
	next.EntryID = currentEntryID
	next.ShowDockFNBadge = Bool(settings.ShowDockFNBadge)
	if requestedEntryID != currentEntryID {
		next.AppName = requestedIdentity.AppName
		next.EntryID = requestedIdentity.EntryID
	}
	next.DisplayName = strings.TrimSpace(input.DisplayName)
	next.Description = strings.TrimSpace(input.Description)
	next.OpenType = NormalizeOpenType(input.OpenType)
	next.Protocol = strings.ToLower(strings.TrimSpace(input.Protocol))
	next.Port = input.Port
	next.Path = normalizedInputPath(input.Path)
	next.AllUsers = input.AllUsers
	next.Revision++
	if next.IconPath, _, err = s.resolveIcon(input, current.IconPath); err != nil {
		return View{}, err
	}
	if err = Validate(next); err != nil {
		return View{}, err
	}
	if err = s.probe(ctx, next.Port); err != nil {
		_ = s.Repo.SetLastError(ctx, id, err.Error())
		return View{}, fmt.Errorf("%w: %v", ErrTargetOffline, err)
	}
	if next.AppName != current.AppName {
		return s.applyRename(ctx, current, next)
	}
	return s.applyUpdate(ctx, current, next, "update")
}

func (s *Service) SuggestIdentity(ctx context.Context, displayName string) (Identity, error) {
	entryID := SuggestEntryID(displayName)
	if entryID == "" {
		return Identity{}, &ValidationError{Fields: []FieldError{{
			Field: "displayName", Message: "does not contain letters, numbers, or supported Chinese characters",
		}}}
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return Identity{}, err
	}
	appName, err := RenderAppNameTemplate(settings.EntryPrefixTemplate, entryID)
	if err != nil {
		return Identity{}, err
	}
	return Identity{EntryID: entryID, AppName: appName}, nil
}

func (s *Service) settings(ctx context.Context) (Settings, error) {
	if s.Settings == nil {
		return DefaultSettings(), nil
	}
	return s.Settings.Get(ctx)
}

func normalizeOrigin(input *Origin) *Origin {
	if input == nil || strings.TrimSpace(input.Source) == "" {
		return &Origin{Source: "manual"}
	}
	origin := &Origin{
		Source:       strings.ToLower(strings.TrimSpace(input.Source)),
		SourceDetail: strings.TrimSpace(input.SourceDetail),
		Description:  strings.TrimSpace(input.Description),
		NetworkMode:  strings.TrimSpace(input.NetworkMode),
		PID:          input.PID,
		WatchCow:     input.WatchCow,
	}
	if origin.Source == "manual" {
		return &Origin{Source: "manual"}
	}
	return origin
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *Service) Repair(ctx context.Context, id string) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Repo.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	next := current
	next.Revision++
	if err = s.probe(ctx, next.Port); err != nil {
		_ = s.Repo.SetLastError(ctx, id, err.Error())
		return View{}, fmt.Errorf("%w: %v", ErrTargetOffline, err)
	}
	return s.applyUpdate(ctx, current, next, "update")
}

// RefreshIcon rebuilds and reinstalls the current registration shell with the
// global badge preference. It deliberately does not probe the target service:
// changing a desktop icon must not require the existing Web service to be up.
func (s *Service) RefreshIcon(ctx context.Context, id string) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Repo.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return View{}, err
	}
	next := current
	next.ShowDockFNBadge = Bool(settings.ShowDockFNBadge)
	next.Revision++
	return s.applyUpdate(ctx, current, next, "update")
}

func (s *Service) Rollback(ctx context.Context, id string) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Repo.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	previous, err := s.previousSpec(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return View{}, ErrNoRollback
		}
		return View{}, err
	}
	previous.ID = current.ID
	previous.AppName = current.AppName
	previous.EntryID = current.EntryID
	previous.Revision = current.Revision + 1
	if err = Validate(previous); err != nil {
		return View{}, err
	}
	if err = s.probe(ctx, previous.Port); err != nil {
		_ = s.Repo.SetLastError(ctx, id, err.Error())
		return View{}, fmt.Errorf("%w: %v", ErrTargetOffline, err)
	}
	return s.applyUpdate(ctx, current, previous, "update")
}

func (s *Service) applyUpdate(ctx context.Context, current, next AppSpec, action string) (View, error) {
	source, err := s.Builder.Render(ctx, next)
	if err != nil {
		_ = s.Repo.SetLastError(ctx, current.ID, err.Error())
		return View{}, err
	}
	defer s.cleanupSource(source.Directory)
	artifact, err := s.Platform.Install(ctx, action, next, source.Directory)
	if err != nil {
		_ = s.Repo.SetLastError(ctx, current.ID, err.Error())
		return View{}, fmt.Errorf("%w: %v", ErrRegistration, err)
	}
	if err = s.commitArtifact(next.ID, artifact.FPKPath, current); err != nil {
		_ = s.Repo.SetLastError(ctx, current.ID, err.Error())
		return View{}, err
	}
	if err = s.Repo.Update(ctx, next); err != nil {
		_ = s.Repo.SetLastError(ctx, current.ID, err.Error())
		return View{}, err
	}
	_ = s.Repo.SetLastError(ctx, next.ID, "")
	return s.check(ctx, next)
}

func (s *Service) applyRename(ctx context.Context, current, next AppSpec) (View, error) {
	source, err := s.Builder.Render(ctx, next)
	if err != nil {
		_ = s.Repo.SetLastError(ctx, current.ID, err.Error())
		return View{}, err
	}
	defer s.cleanupSource(source.Directory)
	artifact, err := s.Platform.Install(ctx, "install", next, source.Directory)
	if err != nil {
		_ = s.Repo.SetLastError(ctx, current.ID, err.Error())
		return View{}, fmt.Errorf("%w: %v", ErrRegistration, err)
	}
	if err = s.Platform.Remove(ctx, current); err != nil {
		cleanupErr := s.Platform.Remove(ctx, next)
		message := fmt.Sprintf("remove previous fnOS registration: %v", err)
		if cleanupErr != nil {
			message += fmt.Sprintf("; cleanup of the new registration also failed: %v", cleanupErr)
		}
		_ = s.Repo.SetLastError(ctx, current.ID, message)
		return View{}, fmt.Errorf("%w: %s", ErrRegistration, message)
	}
	if err = s.commitArtifact(next.ID, artifact.FPKPath, current); err != nil {
		return View{}, s.rollbackRename(ctx, current, next, err)
	}
	if err = s.Repo.Update(ctx, next); err != nil {
		return View{}, s.rollbackRename(ctx, current, next, err)
	}
	_ = s.Repo.SetLastError(ctx, next.ID, "")
	return s.check(ctx, next)
}

func (s *Service) rollbackRename(ctx context.Context, current, next AppSpec, cause error) error {
	rollbackErrors := make([]string, 0, 3)
	if source, renderErr := s.Builder.Render(ctx, current); renderErr != nil {
		rollbackErrors = append(rollbackErrors, "render previous registration: "+renderErr.Error())
	} else {
		defer s.cleanupSource(source.Directory)
		if _, installErr := s.Platform.Install(ctx, "install", current, source.Directory); installErr != nil {
			rollbackErrors = append(rollbackErrors, "restore previous registration: "+installErr.Error())
		}
	}
	if removeErr := s.Platform.Remove(ctx, next); removeErr != nil {
		rollbackErrors = append(rollbackErrors, "remove replacement registration: "+removeErr.Error())
	}
	previousFPK := filepath.Join(s.packagesRoot(), "previous", current.ID+".fpk")
	if _, statErr := os.Stat(previousFPK); statErr == nil {
		if restoreErr := atomicCopy(
			previousFPK,
			filepath.Join(s.packagesRoot(), "current", current.ID+".fpk"),
			0o600,
		); restoreErr != nil {
			rollbackErrors = append(rollbackErrors, "restore previous artifact: "+restoreErr.Error())
		}
	}
	message := cause.Error()
	if len(rollbackErrors) > 0 {
		message += "; rollback incomplete: " + strings.Join(rollbackErrors, "; ")
	} else {
		message += "; previous registration restored"
	}
	_ = s.Repo.SetLastError(ctx, current.ID, message)
	return fmt.Errorf("%w: %s", ErrRegistration, message)
}

func (s *Service) Remove(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, err := s.Repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if !IsManagedSpec(spec) {
		return errors.New("refusing to remove a registration not owned by DockFN")
	}
	if err = s.Platform.Remove(ctx, spec); err != nil {
		_ = s.Repo.SetLastError(ctx, id, err.Error())
		return fmt.Errorf("%w: %v", ErrRegistration, err)
	}
	if err = s.Repo.Delete(ctx, id); err != nil {
		return err
	}
	s.removeArtifacts(id)
	return nil
}

func (s *Service) List(ctx context.Context) ([]View, error) {
	specs, err := s.Repo.List(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(specs))
	for _, spec := range specs {
		view, checkErr := s.check(ctx, spec)
		if checkErr != nil {
			view = View{AppSpec: spec, Status: Status{Registration: "unknown", Target: "unavailable", LastError: checkErr.Error()}}
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool {
		return strings.ToLower(views[i].DisplayName) < strings.ToLower(views[j].DisplayName)
	})
	return views, nil
}

func (s *Service) Get(ctx context.Context, id string) (View, error) {
	spec, err := s.Repo.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	return s.check(ctx, spec)
}

func (s *Service) Check(ctx context.Context, id string) (View, error) {
	return s.Get(ctx, id)
}

func (s *Service) check(ctx context.Context, spec AppSpec) (View, error) {
	view := View{AppSpec: spec, Status: Status{Registration: "missing", Target: "unavailable"}}
	installed, installErr := s.Platform.Installed(ctx, spec)
	if installErr != nil {
		view.Status.Registration = "unknown"
	} else if installed {
		view.Status.Registration = "installed"
	}
	if err := s.probe(ctx, spec.Port); err == nil {
		view.Status.Target = "available"
	}
	view.Status.LastError, _ = s.Repo.LastError(ctx, spec.ID)
	if icon, err := iconDataURL(s.DataDir, spec.IconPath); err == nil {
		view.IconDataURL = icon
	}
	if installErr != nil {
		if view.Status.LastError == "" {
			view.Status.LastError = "Could not verify the fnOS registration: " + installErr.Error()
		}
	}
	return view, nil
}

func (s *Service) probe(ctx context.Context, port uint16) error {
	probe := s.Probe
	if probe == nil {
		probe = TCPProbe
	}
	timeout := s.ProbeTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return probe(probeCtx, port)
}

func (s *Service) resolveIcon(input Input, fallback string) (string, bool, error) {
	base64Value, uriValue := "", ""
	if input.IconBase64 != nil {
		base64Value = strings.TrimSpace(*input.IconBase64)
	}
	if input.IconURI != nil {
		uriValue = strings.TrimSpace(*input.IconURI)
	}
	if base64Value != "" && uriValue != "" {
		return "", false, &ValidationError{Fields: []FieldError{{
			Field: "icon", Message: "choose either an uploaded icon or an icon URI",
		}}}
	}
	if base64Value != "" {
		icon, err := saveIcon(s.DataDir, base64Value)
		if err != nil {
			return "", true, iconValidationError("iconBase64", err)
		}
		return icon, true, nil
	}
	if uriValue != "" {
		icon, err := saveIconURI(
			s.DataDir,
			uriValue,
			strings.ToLower(strings.TrimSpace(input.Protocol)),
			input.Port,
		)
		if err != nil {
			return "", true, iconValidationError("iconUri", err)
		}
		return icon, true, nil
	}
	if input.IconBase64 != nil || input.IconURI != nil {
		return "", true, nil
	}
	return fallback, false, nil
}

func iconValidationError(field string, err error) error {
	return &ValidationError{Fields: []FieldError{{Field: field, Message: err.Error()}}}
}

func TCPProbe(ctx context.Context, port uint16) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if conn != nil {
		_ = conn.Close()
	}
	return err
}

func (s *Service) id() (string, error) {
	if s.NewID != nil {
		return s.NewID()
	}
	body := make([]byte, 6)
	if _, err := rand.Read(body); err != nil {
		return "", err
	}
	return hex.EncodeToString(body), nil
}

func (s *Service) ensureAppNameAvailable(ctx context.Context, appName, excludeID string) error {
	specs, err := s.Repo.List(ctx)
	if err != nil {
		return err
	}
	for _, spec := range specs {
		if spec.ID != excludeID && spec.AppName == appName {
			return &ValidationError{Fields: []FieldError{{
				Field: "entryPrefix", Message: "is already used by another DockFN application",
			}}}
		}
	}
	return nil
}

func normalizedInputPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	return value
}

func (s *Service) packagesRoot() string {
	return filepath.Join(s.DataDir, "packages")
}

func (s *Service) commitArtifact(id, source string, previous AppSpec) error {
	if !idPattern.MatchString(id) {
		return errors.New("invalid artifact ID")
	}
	stagingRoot := s.stagingRoot()
	if !within(stagingRoot, source) {
		return ErrUnsafeArtifact
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("fnOS did not return a valid FPK")
	}
	currentDir := filepath.Join(s.packagesRoot(), "current")
	previousDir := filepath.Join(s.packagesRoot(), "previous")
	if err = os.MkdirAll(currentDir, 0o700); err != nil {
		return err
	}
	if err = os.MkdirAll(previousDir, 0o700); err != nil {
		return err
	}
	currentFPK := filepath.Join(currentDir, id+".fpk")
	if previous.ID != "" {
		if _, statErr := os.Stat(currentFPK); statErr == nil {
			if err = atomicCopy(currentFPK, filepath.Join(previousDir, id+".fpk"), 0o600); err != nil {
				return err
			}
		}
		body, marshalErr := json.MarshalIndent(previous, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		if err = atomicWrite(filepath.Join(previousDir, id+".json"), append(body, '\n'), 0o600); err != nil {
			return err
		}
	}
	return atomicCopy(source, currentFPK, 0o600)
}

func (s *Service) previousSpec(id string) (AppSpec, error) {
	if !idPattern.MatchString(id) {
		return AppSpec{}, ErrNotFound
	}
	body, err := os.ReadFile(filepath.Join(s.packagesRoot(), "previous", id+".json"))
	if err != nil {
		return AppSpec{}, err
	}
	var spec AppSpec
	if err = json.Unmarshal(body, &spec); err != nil {
		return AppSpec{}, err
	}
	spec.OpenType = NormalizeOpenType(spec.OpenType)
	return spec, nil
}

func (s *Service) removeCurrent(id string) {
	if idPattern.MatchString(id) {
		_ = os.Remove(filepath.Join(s.packagesRoot(), "current", id+".fpk"))
	}
}

func (s *Service) removeArtifacts(id string) {
	if !idPattern.MatchString(id) {
		return
	}
	for _, file := range []string{
		filepath.Join(s.packagesRoot(), "current", id+".fpk"),
		filepath.Join(s.packagesRoot(), "previous", id+".fpk"),
		filepath.Join(s.packagesRoot(), "previous", id+".json"),
	} {
		_ = os.Remove(file)
	}
}

func (s *Service) removeOrphanIcon(relative string) {
	if relative == "" {
		return
	}
	specs, err := s.Repo.List(context.Background())
	if err == nil {
		for _, spec := range specs {
			if spec.IconPath == relative {
				return
			}
		}
	}
	path := filepath.Join(s.DataDir, filepath.FromSlash(relative))
	if within(filepath.Join(s.DataDir, "icons"), path) {
		_ = os.RemoveAll(filepath.Dir(path))
	}
}

func (s *Service) cleanupSource(source string) {
	operationRoot := filepath.Dir(source)
	stagingRoot := s.stagingRoot()
	if within(stagingRoot, operationRoot) && operationRoot != stagingRoot {
		_ = os.RemoveAll(operationRoot)
	}
}

func (s *Service) stagingRoot() string {
	if s.StagingDir != "" {
		return s.StagingDir
	}
	return filepath.Join(s.DataDir, "staging")
}

func atomicCopy(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	if err = os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err = tmp.Chmod(mode); err == nil {
		_, err = io.Copy(tmp, input)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, destination)
}

func atomicWrite(destination string, body []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(body)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, destination)
}

func within(root, candidate string) bool {
	rootAbs, rootErr := filepath.Abs(root)
	candidateAbs, candidateErr := filepath.Abs(candidate)
	if rootErr != nil || candidateErr != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, candidateAbs)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
