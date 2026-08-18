package app

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type memoryRepository struct {
	mu     sync.Mutex
	apps   map[string]AppSpec
	errors map[string]string
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{apps: map[string]AppSpec{}, errors: map[string]string{}}
}

func (r *memoryRepository) List(context.Context) ([]AppSpec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]AppSpec, 0, len(r.apps))
	for _, spec := range r.apps {
		result = append(result, spec)
	}
	return result, nil
}
func (r *memoryRepository) Get(_ context.Context, id string) (AppSpec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	spec, ok := r.apps[id]
	if !ok {
		return AppSpec{}, ErrNotFound
	}
	return spec, nil
}
func (r *memoryRepository) Create(_ context.Context, spec AppSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.apps[spec.ID]; exists {
		return errors.New("duplicate")
	}
	r.apps[spec.ID] = spec
	return nil
}
func (r *memoryRepository) Update(_ context.Context, spec AppSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, exists := r.apps[spec.ID]
	if !exists {
		return ErrNotFound
	}
	for id, existing := range r.apps {
		if id != spec.ID && existing.AppName == spec.AppName {
			return errors.New("appName collision")
		}
	}
	_ = current
	r.apps[spec.ID] = spec
	return nil
}
func (r *memoryRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.apps[id]; !exists {
		return ErrNotFound
	}
	delete(r.apps, id)
	return nil
}
func (r *memoryRepository) LastError(_ context.Context, id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.errors[id], nil
}
func (r *memoryRepository) SetLastError(_ context.Context, id, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if message == "" {
		delete(r.errors, id)
	} else {
		r.errors[id] = message
	}
	return nil
}

type fakeBuilder struct {
	staging string
}

func (b *fakeBuilder) Render(_ context.Context, spec AppSpec) (BuildSource, error) {
	root := filepath.Join(b.staging, spec.ID+"-operation", "source")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return BuildSource{}, err
	}
	return BuildSource{Directory: root}, nil
}

type fakePlatform struct {
	mu        sync.Mutex
	installed map[string]bool
	fail      map[string]error
	calls     []string
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{installed: map[string]bool{}, fail: map[string]error{}}
}

func (p *fakePlatform) Install(_ context.Context, action string, spec AppSpec, source string) (InstalledArtifact, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, action+":"+spec.AppName)
	if err := p.fail[action+":"+spec.AppName]; err != nil {
		return InstalledArtifact{}, err
	}
	if err := p.fail[action]; err != nil {
		return InstalledArtifact{}, err
	}
	output := filepath.Join(filepath.Dir(source), "output", spec.AppName+".fpk")
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return InstalledArtifact{}, err
	}
	if err := os.WriteFile(output, []byte("fpk "+spec.AppName), 0o600); err != nil {
		return InstalledArtifact{}, err
	}
	p.installed[spec.AppName] = true
	return InstalledArtifact{FPKPath: output}, nil
}
func (p *fakePlatform) Remove(_ context.Context, spec AppSpec) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "remove:"+spec.AppName)
	if err := p.fail["remove:"+spec.AppName]; err != nil {
		return err
	}
	if err := p.fail["remove"]; err != nil {
		return err
	}
	delete(p.installed, spec.AppName)
	return nil
}
func (p *fakePlatform) Installed(_ context.Context, spec AppSpec) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.installed[spec.AppName], nil
}

func testService(t *testing.T) (*Service, *memoryRepository, *fakePlatform) {
	t.Helper()
	data := t.TempDir()
	staging := filepath.Join(data, "staging")
	repository := newMemoryRepository()
	platform := newFakePlatform()
	service := &Service{
		Repo: repository, Builder: &fakeBuilder{staging: staging}, Platform: platform,
		DataDir: data, StagingDir: staging, Probe: func(context.Context, uint16) error { return nil },
		NewID: func() (string, error) { return "012345abcdef", nil },
	}
	return service, repository, platform
}

func validInput() Input {
	return Input{DisplayName: "Photos", OpenType: "iframe", Protocol: "http", Port: 8080, Path: "/", AllUsers: true}
}

type staticSettings struct {
	value Settings
}

func (s staticSettings) Get(context.Context) (Settings, error) {
	return s.value, nil
}

func TestCreateUsesServerSideGlobalIdentityAndBadgeDefaults(t *testing.T) {
	service, repository, _ := testService(t)
	configured := DefaultSettings()
	configured.EntryPrefixTemplate = "app-{id}"
	configured.ShowDockFNBadge = false
	service.Settings = staticSettings{value: configured}
	input := validInput()
	input.OpenType = ""
	created, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if created.AppName != "app-photos" || created.EntryID != "photos" || created.OpenType != "url" || DockFNBadgeEnabled(created.AppSpec) {
		t.Fatalf("global defaults were not applied: %#v", created.AppSpec)
	}
	stored, err := repository.Get(context.Background(), created.ID)
	if err != nil || stored.ShowDockFNBadge == nil || *stored.ShowDockFNBadge {
		t.Fatalf("badge preference was not persisted: %#v err=%v", stored, err)
	}
}

func TestCreateAndUpdateKeepStableAppName(t *testing.T) {
	service, repository, _ := testService(t)
	configured := DefaultSettings()
	service.Settings = staticSettings{value: configured}
	created, err := service.Create(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if created.AppName != "dkfn.photos" || created.EntryID != "photos" || created.Revision != 1 {
		t.Fatalf("unexpected create result: %#v", created.AppSpec)
	}
	input := validInput()
	input.DisplayName = "Family Photos"
	input.OpenType = "url"
	input.Port = 9090
	configured.ShowDockFNBadge = false
	service.Settings = staticSettings{value: configured}
	updated, err := service.Update(context.Background(), created.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.AppName != created.AppName || updated.Revision != 2 || DockFNBadgeEnabled(updated.AppSpec) {
		t.Fatalf("identity changed during update: %#v", updated.AppSpec)
	}
	stored, _ := repository.Get(context.Background(), created.ID)
	if stored.DisplayName != "Family Photos" || stored.AppName != created.AppName || stored.OpenType != "url" {
		t.Fatalf("unexpected stored update: %#v", stored)
	}
}

func TestRefreshIconAppliesCurrentBadgeWithoutProbingTarget(t *testing.T) {
	service, repository, _ := testService(t)
	created, err := service.Create(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	settings.ShowDockFNBadge = false
	service.Settings = staticSettings{value: settings}
	service.Probe = func(context.Context, uint16) error { return errors.New("target is offline") }

	refreshed, err := service.RefreshIcon(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("refresh icon unexpectedly probed the target: %v", err)
	}
	if refreshed.Revision != created.Revision+1 || DockFNBadgeEnabled(refreshed.AppSpec) ||
		refreshed.AppName != created.AppName || refreshed.Port != created.Port || refreshed.Path != created.Path {
		t.Fatalf("unexpected icon refresh result: %#v", refreshed.AppSpec)
	}
	stored, err := repository.Get(context.Background(), created.ID)
	if err != nil || DockFNBadgeEnabled(stored) {
		t.Fatalf("refreshed badge setting was not persisted: %#v err=%v", stored, err)
	}
}

func TestCreatePersistsSelectedOriginAndUpdatePreservesIt(t *testing.T) {
	service, repository, _ := testService(t)
	input := validInput()
	input.Origin = &Origin{
		Source:       "docker",
		SourceDetail: " photos ",
		Description:  " ghcr.io/example/photos:latest ",
		NetworkMode:  " 1panel-network ",
		WatchCow:     true,
	}
	created, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Origin == nil ||
		created.Origin.Source != "docker" ||
		created.Origin.SourceDetail != "photos" ||
		created.Origin.Description != "ghcr.io/example/photos:latest" ||
		created.Origin.NetworkMode != "1panel-network" ||
		!created.Origin.WatchCow {
		t.Fatalf("origin snapshot was not normalized and persisted: %#v", created.Origin)
	}

	update := validInput()
	update.DisplayName = "Updated Photos"
	updated, err := service.Update(context.Background(), created.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := repository.Get(context.Background(), created.ID)
	if updated.Origin == nil || stored.Origin == nil ||
		updated.Origin.SourceDetail != "photos" ||
		stored.Origin.Description != "ghcr.io/example/photos:latest" {
		t.Fatalf("update replaced the immutable origin snapshot: updated=%#v stored=%#v", updated.Origin, stored.Origin)
	}
}

func TestCreateUsesCustomEntryPrefixAndUpdateMigratesIdentity(t *testing.T) {
	service, repository, platform := testService(t)
	input := validInput()
	input.EntryPrefix = "blinko"
	created, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if created.AppName != "dkfn.blinko" {
		t.Fatalf("custom appName=%q", created.AppName)
	}
	input.EntryPrefix = "other"
	updated, err := service.Update(context.Background(), created.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := repository.Get(context.Background(), created.ID)
	if updated.AppName != "dkfn.other" || stored.AppName != "dkfn.other" || stored.Revision != 2 {
		t.Fatalf("identity migration was not persisted: %#v", stored)
	}
	wantCalls := []string{"install:dkfn.blinko", "install:dkfn.other", "remove:dkfn.blinko"}
	if len(platform.calls) != len(wantCalls) {
		t.Fatalf("calls=%#v", platform.calls)
	}
	for index := range wantCalls {
		if platform.calls[index] != wantCalls[index] {
			t.Fatalf("calls=%#v want=%#v", platform.calls, wantCalls)
		}
	}
}

func TestIdentityMigrationKeepsOldRegistrationWhenRemovalFails(t *testing.T) {
	service, repository, platform := testService(t)
	input := validInput()
	input.EntryPrefix = "old"
	created, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	platform.fail["remove:dkfn.old"] = errors.New("old shell busy")
	input.EntryPrefix = "new"
	if _, err = service.Update(context.Background(), created.ID, input); !errors.Is(err, ErrRegistration) {
		t.Fatalf("wanted registration error, got %v", err)
	}
	stored, _ := repository.Get(context.Background(), created.ID)
	if stored.AppName != "dkfn.old" || !platform.installed["dkfn.old"] || platform.installed["dkfn.new"] {
		t.Fatalf("failed migration state stored=%#v installed=%#v calls=%#v", stored, platform.installed, platform.calls)
	}
}

func TestCreateRejectsDuplicateEntryPrefixBeforePlatformInstall(t *testing.T) {
	service, _, platform := testService(t)
	input := validInput()
	input.EntryPrefix = "photos"
	if _, err := service.Create(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	service.NewID = func() (string, error) { return "fedcba654321", nil }
	if _, err := service.Create(context.Background(), input); err == nil {
		t.Fatal("duplicate entry prefix was accepted")
	} else {
		var validation *ValidationError
		if !errors.As(err, &validation) || validation.Fields[0].Field != "entryPrefix" {
			t.Fatalf("unexpected duplicate error: %v", err)
		}
	}
	if len(platform.calls) != 1 {
		t.Fatalf("duplicate prefix reached platform: %#v", platform.calls)
	}
}

func TestInstallFailureDoesNotSaveConfiguration(t *testing.T) {
	service, repository, platform := testService(t)
	platform.fail["install"] = errors.New("application center unavailable")
	if _, err := service.Create(context.Background(), validInput()); !errors.Is(err, ErrRegistration) {
		t.Fatalf("wanted registration failure, got %v", err)
	}
	items, _ := repository.List(context.Background())
	if len(items) != 0 {
		t.Fatalf("failed create persisted %#v", items)
	}
}

func TestUpdateFailureLeavesOldConfiguration(t *testing.T) {
	service, repository, platform := testService(t)
	created, err := service.Create(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	platform.fail["update"] = errors.New("update rejected")
	input := validInput()
	input.DisplayName = "Should not persist"
	if _, err = service.Update(context.Background(), created.ID, input); !errors.Is(err, ErrRegistration) {
		t.Fatalf("wanted registration failure, got %v", err)
	}
	stored, _ := repository.Get(context.Background(), created.ID)
	if stored.DisplayName != "Photos" || stored.Revision != 1 {
		t.Fatalf("old configuration was changed: %#v", stored)
	}
}

func TestRemoveSuccessAndFailure(t *testing.T) {
	service, repository, platform := testService(t)
	created, err := service.Create(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	platform.fail["remove"] = errors.New("uninstall failed")
	if err = service.Remove(context.Background(), created.ID); !errors.Is(err, ErrRegistration) {
		t.Fatalf("wanted remove failure, got %v", err)
	}
	if _, err = repository.Get(context.Background(), created.ID); err != nil {
		t.Fatalf("failed remove deleted configuration: %v", err)
	}
	delete(platform.fail, "remove")
	if err = service.Remove(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.Get(context.Background(), created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("successful remove retained configuration: %v", err)
	}
}

func TestRemoveRejectsRegistrationOutsideOwnedPrefixes(t *testing.T) {
	service, repository, platform := testService(t)
	repository.apps["012345abcdef"] = AppSpec{
		ID: "012345abcdef", AppName: "watchcow.external", DisplayName: "External",
		Protocol: "http", Port: 8080, Path: "/", Revision: 1,
	}
	if err := service.Remove(context.Background(), "012345abcdef"); err == nil {
		t.Fatal("expected ownership rejection")
	}
	if len(platform.calls) != 0 {
		t.Fatalf("platform was called for external app: %#v", platform.calls)
	}
}

func TestRollbackRestoresPreviousSuccessfulSpec(t *testing.T) {
	service, _, _ := testService(t)
	created, err := service.Create(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	changed := validInput()
	changed.DisplayName = "New name"
	changed.Port = 9090
	updated, err := service.Update(context.Background(), created.ID, changed)
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := service.Rollback(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.DisplayName != "Photos" || rolledBack.Port != 8080 || rolledBack.AppName != created.AppName {
		t.Fatalf("rollback did not restore previous values: %#v", rolledBack.AppSpec)
	}
	if rolledBack.Revision != updated.Revision+1 {
		t.Fatalf("rollback revision=%d, want %d", rolledBack.Revision, updated.Revision+1)
	}
}

func TestTCPProbe(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = TCPProbe(ctx, uint16(port)); err != nil {
		t.Fatalf("open port was reported unavailable: %v", err)
	}
}
