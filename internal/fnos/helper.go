package fnos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dockfn/dockfn/internal/app"
	"github.com/dockfn/dockfn/internal/diagnostics"
	"github.com/dockfn/dockfn/internal/package"
)

type Runner interface {
	Run(context.Context, string, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, directory, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	command.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	output, err := command.CombinedOutput()
	if len(output) > 64<<10 {
		output = output[:64<<10]
	}
	return output, err
}

type Helper struct {
	Socket, StagingDir, DataDir string
	LogDir                      string
	AppCenterCLI, Fnpack        string
	DockerCLI                   string
	InstallVolume               string
	AppRegistryDir              string
	AllowedInstallRoots         []string
	SocketGID                   int
	Runner                      Runner
	WebProbe                    func(context.Context, uint16) (WebProbeResult, error)
	DesktopEntryVerifier        func(string, string, app.AppSpec) error
	ReadProcessCgroup           func(int) ([]byte, error)
	ReadPIDNamespace            func(int) (string, error)
	CommandTimeout              time.Duration
	mu                          sync.Mutex
}

// WebProbeResult is the bounded, read-only metadata collected from one local
// Web service. Keeping the probe behind one result type prevents discovery
// callers from having to understand HTTP parsing details.
type WebProbeResult struct {
	Protocol string
	Title    string
	IconURI  string
}

func (h *Helper) Listen(ctx context.Context) error {
	if !filepath.IsAbs(h.Socket) || !filepath.IsAbs(h.StagingDir) || !filepath.IsAbs(h.DataDir) || !filepath.IsAbs(h.LogDir) {
		return errors.New("helper paths must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(h.Socket), 0o750); err != nil {
		return err
	}
	_ = os.Remove(h.Socket)
	listener, err := net.Listen("unix", h.Socket)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(h.Socket)
	}()
	if h.SocketGID >= 0 {
		if err = os.Chown(h.Socket, 0, h.SocketGID); err != nil {
			return err
		}
	}
	if err = os.Chmod(h.Socket, 0o660); err != nil {
		return err
	}
	server := &http.Server{
		Handler:           h.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      h.timeout() + 5*time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (h *Helper) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/install", h.action("install"))
	mux.HandleFunc("POST /v1/update", h.action("update"))
	mux.HandleFunc("POST /v1/remove", h.action("remove"))
	mux.HandleFunc("GET /v1/discovery", h.discovery)
	mux.HandleFunc("DELETE /v1/diagnostics", h.clearDiagnostics)
	return http.MaxBytesHandler(mux, 8<<10)
}

func (h *Helper) clearDiagnostics(writer http.ResponseWriter, request *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := (diagnostics.Cleaner{LogDir: h.LogDir, DataDir: h.DataDir}).Clear(); err != nil {
		slog.Warn("DockFN diagnostic history clear failed", "error", err)
		writeProblem(writer, http.StatusInternalServerError, "could not clear DockFN diagnostic history")
		return
	}
	slog.Info("DockFN diagnostic history cleared", "capturedAt", time.Now().UTC().Format(time.RFC3339))
	writer.WriteHeader(http.StatusNoContent)
}

func (h *Helper) action(action string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		var input actionRequest
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || !app.IsOwnedAppName(input.AppName) {
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid structured helper request")
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		operationCtx, cancel := context.WithTimeout(request.Context(), h.timeout())
		defer cancel()
		var response actionResponse
		var err error
		switch action {
		case "install", "update":
			response, err = h.install(operationCtx, action, input)
		case "remove":
			response, err = h.remove(operationCtx, input)
		}
		if err != nil {
			slog.Warn("DockFN helper action failed", "action", action, "appName", input.AppName, "error", publicCommandError(err))
			writeProblem(writer, http.StatusBadGateway, publicCommandError(err))
			return
		}
		slog.Info("DockFN helper action completed", "action", action, "appName", input.AppName)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(response)
	}
}

func (h *Helper) install(ctx context.Context, action string, input actionRequest) (actionResponse, error) {
	owned := h.owned(input.AppName)
	if action == "update" && !owned {
		return actionResponse{}, errors.New("registration is not owned by DockFN")
	}
	if action == "install" && owned {
		return actionResponse{}, errors.New("registration already has a DockFN ownership marker")
	}
	source, err := h.sourcePath(input.SourceRelative)
	if err != nil {
		return actionResponse{}, err
	}
	if err = shellpkg.ValidateDirectory(source, input.AppName); err != nil {
		return actionResponse{}, fmt.Errorf("package validation: %w", err)
	}
	expectedSpec, err := shellpkg.ReadSpec(source)
	if err != nil {
		return actionResponse{}, fmt.Errorf("read package specification: %w", err)
	}
	outputDir := filepath.Join(filepath.Dir(source), "fnpack-output")
	if !shellpkg.Within(h.StagingDir, outputDir) {
		return actionResponse{}, errors.New("fnpack output escaped staging")
	}
	_ = os.RemoveAll(outputDir)
	if err = os.MkdirAll(outputDir, 0o750); err != nil {
		return actionResponse{}, err
	}
	output, runErr := h.runner().Run(ctx, outputDir, h.Fnpack, "build", "-d", source)
	if runErr != nil {
		return actionResponse{}, fmt.Errorf("fnpack build failed: %s", redact(output))
	}
	fpkPath := filepath.Join(outputDir, input.AppName+".fpk")
	if err = requireRegularFile(fpkPath); err != nil {
		return actionResponse{}, fmt.Errorf("fnpack output: %w", err)
	}
	if err = h.makeArtifactAccessible(outputDir, fpkPath); err != nil {
		return actionResponse{}, fmt.Errorf("fnpack output permissions: %w", err)
	}
	registrations, err := h.listRegistrations(ctx)
	if err != nil {
		return actionResponse{}, err
	}
	if action == "install" {
		if _, exists := registrations[input.AppName]; exists {
			return actionResponse{}, errors.New("an application with this appName is already installed")
		}
	}
	volume, err := h.volume(ctx)
	if err != nil {
		return actionResponse{}, err
	}
	previousFPK := ""
	replacedExisting := false
	if action == "update" {
		if registration, exists := registrations[input.AppName]; exists {
			if registration.Volume != "" && positiveInteger.MatchString(registration.Volume) {
				volume = registration.Volume
			}
			previousFPK, err = h.currentArtifact(expectedSpec.ID)
			if err != nil {
				return actionResponse{}, fmt.Errorf("prepare fnOS registration update: %w", err)
			}
			output, runErr = h.runner().Run(ctx, "", h.AppCenterCLI, "uninstall", input.AppName)
			if runErr != nil || appCenterFailed(output) {
				return actionResponse{}, fmt.Errorf("application center could not remove the previous registration: %s", redact(output))
			}
			replacedExisting = true
		}
	}
	output, runErr = h.runner().Run(ctx, "", h.AppCenterCLI, "install-fpk", fpkPath, "--volume", volume)
	if runErr != nil || appCenterFailed(output) {
		installErr := fmt.Errorf("application center install failed: %s", redact(output))
		if snapshotErr := h.capturePackageInstallFailure(source, expectedSpec, installErr); snapshotErr != nil {
			slog.Warn("DockFN could not save application center failure diagnostics", "appName", input.AppName, "error", snapshotErr)
		}
		if replacedExisting {
			if restoreErr := h.restorePreviousRegistration(ctx, input.AppName, previousFPK, volume); restoreErr != nil {
				return actionResponse{}, fmt.Errorf("application center install failed: %s; restoring the previous DockFN shell also failed: %v", redact(output), restoreErr)
			}
			return actionResponse{}, fmt.Errorf("application center install failed: %s; restored the previous DockFN shell", redact(output))
		}
		return actionResponse{}, fmt.Errorf("application center install failed: %s", redact(output))
	}
	registrations, err = h.listRegistrations(ctx)
	if err != nil {
		if replacedExisting {
			_ = h.restorePreviousRegistration(ctx, input.AppName, previousFPK, volume)
		}
		return actionResponse{}, fmt.Errorf("verify application center install: %w", err)
	}
	registration, exists := registrations[input.AppName]
	if !exists {
		if replacedExisting {
			if restoreErr := h.restorePreviousRegistration(ctx, input.AppName, previousFPK, volume); restoreErr != nil {
				return actionResponse{}, fmt.Errorf("application center did not report the installed registration; restoring the previous DockFN shell also failed: %v", restoreErr)
			}
			return actionResponse{}, errors.New("application center did not report the installed registration; restored the previous DockFN shell")
		}
		return actionResponse{}, errors.New("application center did not report the installed registration")
	}
	if registration.Volume != "" && positiveInteger.MatchString(registration.Volume) {
		volume = registration.Volume
	}
	layout, err := h.observedInstallLayout(input.AppName, volume)
	if err != nil {
		if replacedExisting {
			_ = h.restorePreviousRegistration(ctx, input.AppName, previousFPK, volume)
		}
		return actionResponse{}, fmt.Errorf("verify application center install path: %w", err)
	}
	installPath := layout.TargetRoot
	if err = h.verifyDesktopEntry(layout.RegistryRoot, layout.TargetRoot, expectedSpec); err != nil {
		if snapshotErr := h.captureDesktopValidationFailure(layout, expectedSpec, err); snapshotErr != nil {
			slog.Warn("DockFN could not save desktop validation diagnostics", "appName", input.AppName, "error", snapshotErr)
		}
		if replacedExisting {
			if restoreErr := h.restorePreviousRegistration(ctx, input.AppName, previousFPK, volume); restoreErr != nil {
				return actionResponse{}, fmt.Errorf("verify fnOS desktop entry: %w; restoring the previous DockFN shell also failed: %v", err, restoreErr)
			}
			return actionResponse{}, fmt.Errorf("verify fnOS desktop entry: %w; restored the previous DockFN shell", err)
		}
		if cleanupErr := h.removeInstalledRegistration(ctx, input.AppName); cleanupErr != nil {
			return actionResponse{}, fmt.Errorf("verify fnOS desktop entry: %w; automatic cleanup of the new DockFN shell failed: %v", err, cleanupErr)
		}
		return actionResponse{}, fmt.Errorf("verify fnOS desktop entry: %w; removed the incomplete DockFN shell", err)
	}
	digest, err := fileSHA256(fpkPath)
	if err != nil {
		return actionResponse{}, err
	}
	marker := ownership{
		AppName: input.AppName, GeneratedBy: "dockfn", InstallPath: installPath, ArtifactSHA256: digest,
	}
	if err = h.writeOwnership(marker); err != nil {
		return actionResponse{}, err
	}
	relative, err := filepath.Rel(h.StagingDir, fpkPath)
	if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, "..") {
		return actionResponse{}, errors.New("built FPK escaped staging")
	}
	return actionResponse{
		FPKRelative: filepath.ToSlash(relative), InstallPath: installPath,
		Message: "fnOS registration installed",
	}, nil
}

func (h *Helper) currentArtifact(id string) (string, error) {
	if !regexp.MustCompile(`^[a-f0-9]{12}$`).MatchString(id) {
		return "", errors.New("invalid previous artifact ID")
	}
	root := filepath.Join(h.DataDir, "packages", "current")
	path := filepath.Join(root, id+".fpk")
	if !shellpkg.Within(root, path) {
		return "", errors.New("previous artifact escaped DockFN data")
	}
	if err := requireRegularFile(path); err != nil {
		return "", fmt.Errorf("previous successful FPK is unavailable: %w", err)
	}
	return path, nil
}

func (h *Helper) removeInstalledRegistration(ctx context.Context, appName string) error {
	registrations, err := h.listRegistrations(ctx)
	if err != nil {
		return err
	}
	if _, exists := registrations[appName]; !exists {
		return nil
	}
	output, runErr := h.runner().Run(ctx, "", h.AppCenterCLI, "uninstall", appName)
	if runErr != nil || appCenterFailed(output) {
		return fmt.Errorf("application center uninstall failed: %s", redact(output))
	}
	return nil
}

func (h *Helper) restorePreviousRegistration(ctx context.Context, appName, previousFPK, volume string) error {
	if err := h.removeInstalledRegistration(ctx, appName); err != nil {
		return err
	}
	output, runErr := h.runner().Run(ctx, "", h.AppCenterCLI, "install-fpk", previousFPK, "--volume", volume)
	if runErr != nil || appCenterFailed(output) {
		return fmt.Errorf("application center restore failed: %s", redact(output))
	}
	registrations, err := h.listRegistrations(ctx)
	if err != nil {
		return err
	}
	if _, exists := registrations[appName]; !exists {
		return errors.New("application center did not report the restored registration")
	}
	return nil
}

func (h *Helper) verifyDesktopEntry(registryRoot, targetRoot string, spec app.AppSpec) error {
	if h.DesktopEntryVerifier != nil {
		return h.DesktopEntryVerifier(registryRoot, targetRoot, spec)
	}
	return verifyDesktopEntry(registryRoot, targetRoot, spec)
}

type desktopEntry struct {
	Title    string            `json:"title"`
	Icon     string            `json:"icon"`
	Type     string            `json:"type"`
	Protocol string            `json:"protocol"`
	Port     string            `json:"port"`
	URL      string            `json:"url"`
	AllUsers bool              `json:"allUsers"`
	Control  map[string]string `json:"control,omitempty"`
}

func verifyDesktopEntry(registryRoot, targetRoot string, spec app.AppSpec) error {
	if err := app.Validate(spec); err != nil {
		return errors.New("invalid desktop entry appName")
	}
	manifestPath := filepath.Join(registryRoot, "manifest")
	manifestBody, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", manifestPath, err)
	}
	manifest := parseManifest(manifestBody)
	entryName := app.DesktopEntryName(spec.AppName)
	if manifest["appname"] != spec.AppName || manifest["desktop_uidir"] != "ui" ||
		manifest["desktop_applaunchname"] != entryName ||
		manifest["service_port"] != strconv.FormatUint(uint64(spec.Port), 10) ||
		manifest["ctl_stop"] != "true" {
		return fmt.Errorf("%s does not describe the expected desktop entry", manifestPath)
	}
	configPath := filepath.Join(targetRoot, "ui", "config")
	body, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}
	var config struct {
		URL map[string]json.RawMessage `json:".url"`
	}
	if err = json.Unmarshal(body, &config); err != nil {
		return fmt.Errorf("decode %s: %w", configPath, err)
	}
	raw, exists := config.URL[entryName]
	if !exists {
		return fmt.Errorf("%s has no .url entry for %s", configPath, entryName)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("decode %s entry: %w", configPath, err)
	}
	if _, legacy := fields["path"]; legacy {
		return fmt.Errorf("%s uses unsupported path instead of url", configPath)
	}
	var entry desktopEntry
	if err = json.Unmarshal(raw, &entry); err != nil {
		return fmt.Errorf("decode %s entry fields: %w", configPath, err)
	}
	if strings.TrimSpace(entry.Title) == "" || entry.Icon != "images/icon_{0}.png" ||
		entry.Type != app.NormalizeOpenType(spec.OpenType) || entry.Protocol != spec.Protocol ||
		entry.Port != strconv.FormatUint(uint64(spec.Port), 10) || entry.URL != spec.Path ||
		entry.AllUsers != spec.AllUsers {
		return fmt.Errorf("%s has an incomplete desktop entry", configPath)
	}
	if !spec.AllUsers && entry.Control["accessPerm"] != "readonly" {
		return fmt.Errorf("%s does not restrict the administrator-only entry", configPath)
	}
	for _, name := range []string{"icon_64.png", "icon_256.png"} {
		path := filepath.Join(targetRoot, "ui", "images", name)
		file, openErr := os.Open(path)
		if openErr != nil {
			return fmt.Errorf("desktop icon %s is unavailable", path)
		}
		config, decodeErr := png.DecodeConfig(file)
		_ = file.Close()
		expectedSize := 64
		if name == "icon_256.png" {
			expectedSize = 256
		}
		if decodeErr != nil || config.Width != expectedSize || config.Height != expectedSize {
			return fmt.Errorf("desktop icon %s is not a %dx%d PNG", path, expectedSize, expectedSize)
		}
	}
	return nil
}

type desktopValidationSnapshot struct {
	CapturedAt   string      `json:"capturedAt"`
	Stage        string      `json:"stage"`
	AppName      string      `json:"appName"`
	RegistryPath string      `json:"registryPath"`
	InstallPath  string      `json:"installPath"`
	Error        string      `json:"error"`
	Expected     app.AppSpec `json:"expected"`
	Manifest     string      `json:"manifest,omitempty"`
	UIConfig     string      `json:"uiConfig,omitempty"`
}

func (h *Helper) captureDesktopValidationFailure(layout installedLayout, spec app.AppSpec, validationErr error) error {
	snapshot := desktopValidationSnapshot{
		CapturedAt: time.Now().UTC().Format(time.RFC3339), AppName: spec.AppName,
		RegistryPath: layout.RegistryRoot, InstallPath: layout.TargetRoot,
		Stage: "desktop-validation", Error: validationErr.Error(), Expected: spec,
	}
	snapshot.Manifest = readDiagnosticFile(filepath.Join(layout.RegistryRoot, "manifest"))
	snapshot.UIConfig = readDiagnosticFile(filepath.Join(layout.TargetRoot, "ui", "config"))
	return h.writeDiagnostic("last-install-failure.json", snapshot)
}

func (h *Helper) capturePackageInstallFailure(source string, spec app.AppSpec, installErr error) error {
	snapshot := desktopValidationSnapshot{
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Stage:      "appcenter-install",
		AppName:    spec.AppName,
		Error:      installErr.Error(),
		Expected:   spec,
		Manifest:   readDiagnosticFile(filepath.Join(source, "manifest")),
		UIConfig:   readDiagnosticFile(filepath.Join(source, "ui", "config")),
	}
	return h.writeDiagnostic("last-install-failure.json", snapshot)
}

func readDiagnosticFile(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(body) > 64<<10 {
		body = body[:64<<10]
	}
	return string(body)
}

func (h *Helper) writeDiagnostic(name string, value any) error {
	if name != "last-install-failure.json" && name != "last-discovery.json" {
		return errors.New("unsupported diagnostic name")
	}
	if h.DataDir == "" {
		return nil
	}
	if !filepath.IsAbs(h.DataDir) {
		return errors.New("diagnostic data directory must be absolute")
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Join(h.DataDir, "diagnostics")
	if err = os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".diagnostic-*.tmp")
	if err != nil {
		return err
	}
	path := temporary.Name()
	defer func() { _ = os.Remove(path) }()
	if err = temporary.Chmod(0o640); err == nil {
		_, err = temporary.Write(append(body, '\n'))
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(directory, name))
}

func parseManifest(body []byte) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		key = strings.TrimSpace(key)
		if ok && key != "" {
			result[key] = strings.TrimSpace(value)
		}
	}
	return result
}

func (h *Helper) remove(ctx context.Context, input actionRequest) (actionResponse, error) {
	if input.SourceRelative != "" {
		return actionResponse{}, errors.New("remove does not accept a source path")
	}
	if !h.owned(input.AppName) {
		return actionResponse{}, errors.New("registration is not owned by DockFN")
	}
	registrations, err := h.listRegistrations(ctx)
	if err != nil {
		return actionResponse{}, err
	}
	if _, exists := registrations[input.AppName]; !exists {
		if err = os.Remove(h.ownershipPath(input.AppName)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return actionResponse{}, err
		}
		return actionResponse{Message: "registration was already absent"}, nil
	}
	output, runErr := h.runner().Run(ctx, "", h.AppCenterCLI, "uninstall", input.AppName)
	if runErr != nil || appCenterFailed(output) {
		return actionResponse{}, fmt.Errorf("application center remove failed: %s", redact(output))
	}
	registrations, err = h.listRegistrations(ctx)
	if err != nil {
		return actionResponse{}, err
	}
	if _, exists := registrations[input.AppName]; exists {
		return actionResponse{}, errors.New("application center still reports the registration")
	}
	if err = os.Remove(h.ownershipPath(input.AppName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return actionResponse{}, err
	}
	return actionResponse{Message: "fnOS registration removed; target service and data were not touched"}, nil
}

func (h *Helper) sourcePath(relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || strings.Contains(relative, `\`) {
		return "", errors.New("sourceRelative must be a safe relative path")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("sourceRelative escaped staging")
	}
	source := filepath.Join(h.StagingDir, clean)
	if !shellpkg.Within(h.StagingDir, source) {
		return "", errors.New("sourceRelative escaped staging")
	}
	return source, nil
}

func (h *Helper) listRegistrations(ctx context.Context) (map[string]registration, error) {
	output, err := h.runner().Run(ctx, "", h.AppCenterCLI, "list", "--json")
	if err == nil {
		if result, ok := parseRegistrationJSON(output); ok {
			return result, nil
		}
	}
	output, err = h.runner().Run(ctx, "", h.AppCenterCLI, "list")
	if err != nil {
		return nil, fmt.Errorf("application center list failed: %s", redact(output))
	}
	result := parseRegistrationTable(output)
	if len(result) == 0 && strings.TrimSpace(string(output)) != "" &&
		!strings.Contains(string(output), "|") && !strings.Contains(string(output), "│") {
		return nil, errors.New("application center returned an unrecognized list")
	}
	return result, nil
}

type registration struct {
	AppName string
	Volume  string
}

func parseRegistrationJSON(body []byte) (map[string]registration, bool) {
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		var envelope struct {
			Items []map[string]any `json:"items"`
		}
		if json.Unmarshal(body, &envelope) != nil {
			return nil, false
		}
		rows = envelope.Items
	}
	result := map[string]registration{}
	for _, row := range rows {
		name := fmt.Sprint(row["appname"])
		if !observedAppName.MatchString(name) {
			continue
		}
		result[name] = registration{AppName: name, Volume: numericString(row["volume"])}
	}
	return result, true
}

func parseRegistrationTable(body []byte) map[string]registration {
	result := map[string]registration{}
	for _, line := range strings.Split(string(body), "\n") {
		separator := "|"
		if strings.Contains(line, "│") {
			separator = "│"
		}
		if !strings.Contains(line, separator) {
			continue
		}
		fields := strings.Split(line, separator)
		values := make([]string, 0, len(fields))
		for _, field := range fields {
			if value := strings.TrimSpace(field); value != "" {
				values = append(values, value)
			}
		}
		if len(values) == 0 || !observedAppName.MatchString(values[0]) || strings.EqualFold(values[0], "appname") {
			continue
		}
		entry := registration{AppName: values[0]}
		for _, value := range values[1:] {
			if positiveInteger.MatchString(value) {
				entry.Volume = value
			}
		}
		result[entry.AppName] = entry
	}
	return result
}

func numericString(value any) string {
	text := fmt.Sprint(value)
	if positiveInteger.MatchString(text) {
		return text
	}
	return ""
}

func (h *Helper) volume(ctx context.Context) (string, error) {
	value := strings.TrimSpace(h.InstallVolume)
	if value == "" || value == "auto" {
		value = "1"
		if output, err := h.runner().Run(ctx, "", h.AppCenterCLI, "default-volume"); err == nil {
			if candidate := strings.TrimSpace(string(output)); positiveInteger.MatchString(candidate) {
				value = candidate
			}
		}
	}
	if !positiveInteger.MatchString(value) {
		return "", errors.New("invalid application center install volume")
	}
	return value, nil
}

type installedLayout struct {
	RegistryRoot string
	TargetRoot   string
}

func (h *Helper) observedInstallLayout(appName, fallbackVolume string) (installedLayout, error) {
	if !app.IsOwnedAppName(appName) || !positiveInteger.MatchString(fallbackVolume) {
		return installedLayout{}, errors.New("invalid install path input")
	}
	fallbackRoot := filepath.Join("/vol"+fallbackVolume, "@appcenter")
	registryDir := h.AppRegistryDir
	if registryDir == "" {
		registryDir = "/var/apps"
	} else if !filepath.IsAbs(registryDir) {
		return installedLayout{}, errors.New("application registry path is not absolute")
	}
	registryRoot := filepath.Join(filepath.Clean(registryDir), appName)
	link := filepath.Join(registryRoot, "target")
	resolved, err := filepath.EvalSymlinks(link)
	if errors.Is(err, os.ErrNotExist) {
		return installedLayout{RegistryRoot: registryRoot, TargetRoot: filepath.Join(fallbackRoot, appName)}, nil
	}
	if err != nil {
		return installedLayout{}, err
	}
	roots := append([]string(nil), h.AllowedInstallRoots...)
	if len(roots) == 0 {
		roots = []string{fallbackRoot, "/usr/local/apps/@appcenter"}
	}
	resolved = filepath.Clean(resolved)
	for _, root := range roots {
		root = filepath.Clean(root)
		if filepath.IsAbs(root) && resolved == filepath.Join(root, appName) && shellpkg.Within(root, resolved) {
			return installedLayout{RegistryRoot: registryRoot, TargetRoot: resolved}, nil
		}
	}
	return installedLayout{}, errors.New("application center reported an install path outside approved roots")
}

func (h *Helper) observedInstallPath(appName, fallbackVolume string) (string, error) {
	layout, err := h.observedInstallLayout(appName, fallbackVolume)
	return layout.TargetRoot, err
}

func (h *Helper) makeArtifactAccessible(outputDir, fpkPath string) error {
	if h.SocketGID <= 0 {
		return nil
	}
	if !shellpkg.Within(h.StagingDir, outputDir) || !shellpkg.Within(outputDir, fpkPath) {
		return errors.New("artifact permissions escaped staging")
	}
	if err := os.Chown(outputDir, 0, h.SocketGID); err != nil {
		return err
	}
	if err := os.Chmod(outputDir, 0o770); err != nil {
		return err
	}
	if err := os.Chown(fpkPath, 0, h.SocketGID); err != nil {
		return err
	}
	return os.Chmod(fpkPath, 0o660)
}

func (h *Helper) runner() Runner {
	if h.Runner == nil {
		return ExecRunner{}
	}
	return h.Runner
}

func (h *Helper) timeout() time.Duration {
	if h.CommandTimeout <= 0 {
		return 45 * time.Second
	}
	return h.CommandTimeout
}

func (h *Helper) ownershipPath(appName string) string {
	return filepath.Join(h.DataDir, "ownership", appName+".json")
}

func (h *Helper) owned(appName string) bool {
	marker, err := readOwnership(h.DataDir, appName)
	return err == nil && marker.GeneratedBy == "dockfn" && marker.AppName == appName
}

func (h *Helper) writeOwnership(marker ownership) error {
	if !app.IsOwnedAppName(marker.AppName) || marker.GeneratedBy != "dockfn" {
		return errors.New("invalid ownership marker")
	}
	body, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	path := h.ownershipPath(marker.AppName)
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if h.SocketGID >= 0 {
		_ = os.Chown(filepath.Dir(path), 0, h.SocketGID)
		_ = os.Chmod(filepath.Dir(path), 0o770)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".ownership-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err = temporary.Chmod(0o640); err == nil {
		_, err = temporary.Write(append(body, '\n'))
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	if h.SocketGID >= 0 {
		if err = os.Chown(path, 0, h.SocketGID); err != nil {
			return err
		}
	}
	return os.Chmod(path, 0o640)
}

func requireRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() == 0 {
		return errors.New("artifact must be a non-empty regular file")
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeProblem(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"message": message})
}

func publicCommandError(err error) string {
	message := err.Error()
	lower := strings.ToLower(message)
	for _, sensitive := range []string{"token", "cookie", "authorization", "password", "credential"} {
		if strings.Contains(lower, sensitive) {
			return "fnOS command failed; inspect the DockFN helper log"
		}
	}
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}

func redact(body []byte) string {
	value := strings.TrimSpace(string(body))
	if value == "" {
		return "command returned no diagnostic output"
	}
	return publicCommandError(errors.New(value))
}

func appCenterFailed(body []byte) bool {
	value := strings.ToLower(string(body))
	return strings.Contains(value, "[error]") ||
		strings.Contains(value, "something wrong with appcenter") ||
		strings.Contains(value, "failed to launch app")
}

var (
	observedAppName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	positiveInteger = regexp.MustCompile(`^[1-9][0-9]*$`)
)

func ParseSocketGID(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return -1, nil
	}
	gid, err := strconv.Atoi(value)
	if err != nil || gid < 0 {
		return 0, errors.New("DOCKFN_HELPER_GID must be a non-negative integer")
	}
	return gid, nil
}
