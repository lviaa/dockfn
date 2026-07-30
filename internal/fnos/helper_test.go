package fnos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dockfn/dockfn/internal/app"
	shellpkg "github.com/dockfn/dockfn/internal/package"
)

type helperRunner struct {
	mu             sync.Mutex
	registrations  map[string]registration
	calls          []string
	failNewInstall bool
}

func (r *helperRunner) Run(_ context.Context, directory, name string, arguments ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, filepath.Base(name)+" "+strings.Join(arguments, " "))
	switch filepath.Base(name) {
	case "fnpack":
		source := arguments[len(arguments)-1]
		manifest, err := os.ReadFile(filepath.Join(source, "manifest"))
		if err != nil {
			return nil, err
		}
		appName := ""
		for _, line := range strings.Split(string(manifest), "\n") {
			if strings.HasPrefix(line, "appname=") {
				appName = strings.TrimPrefix(line, "appname=")
			}
		}
		if appName == "" {
			return nil, errors.New("manifest missing appname")
		}
		return []byte("built"), os.WriteFile(filepath.Join(directory, appName+".fpk"), []byte("fpk"), 0o640)
	case "appcenter-cli":
		switch arguments[0] {
		case "list":
			rows := make([]map[string]string, 0, len(r.registrations))
			for _, item := range r.registrations {
				rows = append(rows, map[string]string{"appname": item.AppName, "volume": item.Volume, "status": "installed"})
			}
			return json.Marshal(rows)
		case "default-volume":
			return []byte("1\n"), nil
		case "install-fpk":
			appName := strings.TrimSuffix(filepath.Base(arguments[1]), ".fpk")
			if strings.Contains(filepath.ToSlash(arguments[1]), "/packages/current/") {
				body, err := os.ReadFile(arguments[1])
				if err != nil {
					return nil, err
				}
				appName = strings.TrimSpace(string(body))
			}
			if r.failNewInstall && strings.Contains(arguments[1], "fnpack-output") {
				r.failNewInstall = false
				return []byte("install rejected"), errors.New("install rejected")
			}
			if _, exists := r.registrations[appName]; exists {
				return []byte("already installed"), nil
			}
			r.registrations[appName] = registration{AppName: appName, Volume: "1"}
			return []byte("installed"), nil
		case "uninstall":
			delete(r.registrations, arguments[1])
			return []byte("removed"), nil
		}
	}
	return nil, errors.New("unexpected command")
}

func helperFixture(t *testing.T) (*Helper, app.AppSpec, string, *helperRunner) {
	return helperFixtureWithAppName(t, "photos.dkfn")
}

func helperFixtureWithAppName(t *testing.T, appName string) (*Helper, app.AppSpec, string, *helperRunner) {
	t.Helper()
	data := t.TempDir()
	staging := filepath.Join(data, "staging")
	spec := app.AppSpec{
		ID: "012345abcdef", AppName: appName, DisplayName: "Photos",
		OpenType: "url", Protocol: "http", Port: 8080, Path: "/", Revision: 1,
	}
	builder := &shellpkg.Builder{DataDir: data, StagingDir: staging}
	source, err := builder.Render(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	runner := &helperRunner{registrations: map[string]registration{}}
	helper := &Helper{
		StagingDir: staging, DataDir: data, LogDir: filepath.Join(data, "logs"), AppCenterCLI: "appcenter-cli",
		Fnpack: "fnpack", InstallVolume: "auto", SocketGID: -1, Runner: runner,
	}
	relative, err := filepath.Rel(staging, source.Directory)
	if err != nil {
		t.Fatal(err)
	}
	return helper, spec, filepath.ToSlash(relative), runner
}

func TestHelperUpdateUsesInternalIDForCustomEntryPrefixArtifact(t *testing.T) {
	helper, spec, relative, _ := helperFixtureWithAppName(t, "blinko.dkfn")
	helper.DesktopEntryVerifier = func(string, string, app.AppSpec) error { return nil }
	if _, err := helper.install(context.Background(), "install", actionRequest{AppName: spec.AppName, SourceRelative: relative}); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(helper.DataDir, "packages", "current", spec.ID+".fpk")
	if err := os.MkdirAll(filepath.Dir(current), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte(spec.AppName), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := helper.install(context.Background(), "update", actionRequest{AppName: spec.AppName, SourceRelative: relative}); err != nil {
		t.Fatalf("custom-prefix update could not find the ID-keyed artifact: %v", err)
	}
}

func TestHelperInstallsNewDomainIdentity(t *testing.T) {
	helper, spec, relative, runner := helperFixtureWithAppName(t, "blinko.dkfn")
	helper.DesktopEntryVerifier = func(_, _ string, expected app.AppSpec) error {
		if expected.AppName != "blinko.dkfn" || app.DesktopEntryName(expected.AppName) != "blinko.dkfn" {
			return errors.New("unexpected desktop identity")
		}
		return nil
	}
	if _, err := helper.install(context.Background(), "install", actionRequest{
		AppName: spec.AppName, SourceRelative: relative,
	}); err != nil {
		t.Fatal(err)
	}
	if _, exists := runner.registrations["blinko.dkfn"]; !exists || !helper.owned("blinko.dkfn") {
		t.Fatalf("new identity was not installed or owned: %#v", runner.registrations)
	}
}

func TestHelperInstallUpdateAndRemove(t *testing.T) {
	helper, spec, relative, runner := helperFixture(t)
	verifiedDesktopEntry := false
	helper.DesktopEntryVerifier = func(registryPath, targetPath string, expected app.AppSpec) error {
		verifiedDesktopEntry = registryPath != "" && targetPath != "" && expected == spec
		return nil
	}
	response, err := helper.install(context.Background(), "install", actionRequest{AppName: spec.AppName, SourceRelative: relative})
	if err != nil {
		t.Fatal(err)
	}
	if response.FPKRelative == "" || !helper.owned(spec.AppName) {
		t.Fatalf("install did not produce artifact and ownership: %#v", response)
	}
	if !verifiedDesktopEntry {
		t.Fatal("install did not verify the desktop entry")
	}
	marker, err := readOwnership(helper.DataDir, spec.AppName)
	if err != nil || marker.InstallPath != filepath.Join("/vol1", "@appcenter", spec.AppName) {
		t.Fatalf("unexpected fallback install path: marker=%#v err=%v", marker, err)
	}
	if _, exists := runner.registrations[spec.AppName]; !exists {
		t.Fatal("fake application center did not install registration")
	}
	current := filepath.Join(helper.DataDir, "packages", "current", spec.ID+".fpk")
	if err = os.MkdirAll(filepath.Dir(current), 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(current, []byte(spec.AppName), 0o640); err != nil {
		t.Fatal(err)
	}
	runner.calls = nil
	if _, err = helper.install(context.Background(), "update", actionRequest{AppName: spec.AppName, SourceRelative: relative}); err != nil {
		t.Fatal(err)
	}
	commands := strings.Join(runner.calls, "\n")
	if !strings.Contains(commands, "appcenter-cli uninstall "+spec.AppName) ||
		!strings.Contains(commands, "appcenter-cli install-fpk") {
		t.Fatalf("update did not replace the installed FPK: %s", commands)
	}
	if _, err = helper.remove(context.Background(), actionRequest{AppName: spec.AppName}); err != nil {
		t.Fatal(err)
	}
	if helper.owned(spec.AppName) {
		t.Fatal("remove retained ownership marker")
	}
	if _, exists := runner.registrations[spec.AppName]; exists {
		t.Fatal("remove retained registration")
	}
}

func TestHelperUpdateRestoresPreviousRegistrationWhenNewInstallFails(t *testing.T) {
	helper, spec, relative, runner := helperFixture(t)
	helper.DesktopEntryVerifier = func(string, string, app.AppSpec) error { return nil }
	if _, err := helper.install(context.Background(), "install", actionRequest{AppName: spec.AppName, SourceRelative: relative}); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(helper.DataDir, "packages", "current", spec.ID+".fpk")
	if err := os.MkdirAll(filepath.Dir(current), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte(spec.AppName), 0o640); err != nil {
		t.Fatal(err)
	}
	runner.calls = nil
	runner.failNewInstall = true
	if _, err := helper.install(context.Background(), "update", actionRequest{AppName: spec.AppName, SourceRelative: relative}); err == nil ||
		!strings.Contains(err.Error(), "restored the previous DockFN shell") {
		t.Fatalf("update failure did not report restoration: %v", err)
	}
	if _, exists := runner.registrations[spec.AppName]; !exists {
		t.Fatal("failed update did not restore the previous registration")
	}
	commands := strings.Join(runner.calls, "\n")
	if !strings.Contains(commands, current) {
		t.Fatalf("previous artifact was not reinstalled: %s", commands)
	}
}

func TestHelperInstallFailureReplacesStaleDiagnostic(t *testing.T) {
	helper, spec, relative, runner := helperFixtureWithAppName(t, "demo.dkfn")
	diagnostic := filepath.Join(helper.DataDir, "diagnostics", "last-install-failure.json")
	if err := os.MkdirAll(filepath.Dir(diagnostic), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(diagnostic, []byte(`{"capturedAt":"2020-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner.failNewInstall = true
	if _, err := helper.install(context.Background(), "install", actionRequest{
		AppName: spec.AppName, SourceRelative: relative,
	}); err == nil {
		t.Fatal("expected application center failure")
	}
	body, err := os.ReadFile(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot desktopValidationSnapshot
	if err = json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.CapturedAt == "2020-01-01T00:00:00Z" || snapshot.Stage != "appcenter-install" ||
		snapshot.AppName != spec.AppName || snapshot.Expected != spec ||
		!strings.Contains(snapshot.Error, "application center install failed") {
		t.Fatalf("current install failure was not captured: %#v", snapshot)
	}
}

func TestHelperUpdateRestoresPreviousRegistrationWhenDesktopValidationFails(t *testing.T) {
	helper, spec, relative, runner := helperFixture(t)
	helper.DesktopEntryVerifier = func(string, string, app.AppSpec) error { return nil }
	if _, err := helper.install(context.Background(), "install", actionRequest{AppName: spec.AppName, SourceRelative: relative}); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(helper.DataDir, "packages", "current", spec.ID+".fpk")
	if err := os.MkdirAll(filepath.Dir(current), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte(spec.AppName), 0o640); err != nil {
		t.Fatal(err)
	}
	runner.calls = nil
	helper.DesktopEntryVerifier = func(string, string, app.AppSpec) error {
		return errors.New("replacement desktop entry is invalid")
	}
	if _, err := helper.install(context.Background(), "update", actionRequest{AppName: spec.AppName, SourceRelative: relative}); err == nil ||
		!strings.Contains(err.Error(), "restored the previous DockFN shell") {
		t.Fatalf("desktop validation failure did not restore the previous registration: %v", err)
	}
	if _, exists := runner.registrations[spec.AppName]; !exists {
		t.Fatal("desktop validation failure left no registration")
	}
	if !strings.Contains(strings.Join(runner.calls, "\n"), current) {
		t.Fatal("desktop validation failure did not reinstall the previous artifact")
	}
}

func TestHelperRejectsStagingEscapeAndExternalRemoval(t *testing.T) {
	helper, _, _, _ := helperFixture(t)
	if _, err := helper.sourcePath("../../outside"); err == nil {
		t.Fatal("staging traversal accepted")
	}
	if _, err := helper.remove(context.Background(), actionRequest{AppName: "dockfn.unowned"}); err == nil {
		t.Fatal("unowned registration removal accepted")
	}
}

func TestObservedInstallPathUsesRegistryTargetWithinApprovedRoot(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("absolute fnOS symlink semantics require Unix")
	}
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	appCenter := filepath.Join(root, "appcenter")
	appName := "photos.dkfn"
	target := filepath.Join(appCenter, appName)
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(registry, appName)
	if err := os.MkdirAll(linkDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(linkDir, "target")); err != nil {
		t.Fatal(err)
	}
	helper := &Helper{AppRegistryDir: registry, AllowedInstallRoots: []string{appCenter}}
	observed, err := helper.observedInstallPath(appName, "1")
	if err != nil || observed != target {
		t.Fatalf("observed path=%q err=%v, want %q", observed, err, target)
	}

	helper.AllowedInstallRoots = []string{filepath.Join(root, "different-root")}
	if _, err = helper.observedInstallPath(appName, "1"); err == nil {
		t.Fatal("unapproved application center path accepted")
	}
}

func TestVerifyDesktopEntryReadsManifestFromRegistryAndUIFromTarget(t *testing.T) {
	registryRoot := t.TempDir()
	targetRoot := t.TempDir()
	spec := app.AppSpec{
		ID: "012345abcdef", AppName: "photos.dkfn", DisplayName: "Photos",
		Protocol: "http", Port: 8080, Path: "/", Revision: 1,
	}
	writeValidDesktopFixture(t, registryRoot, targetRoot, spec)
	installedManifest := fmt.Sprintf(
		"appname               = %s\n"+
			"desktop_uidir         = ui\n"+
			"desktop_applaunchname = %s\n"+
			"service_port          = %d\n"+
			"ctl_stop              = true\n"+
			"checksum              = 58783f3327ec57d2d2dc2d470cb8f81c\n",
		spec.AppName, spec.AppName, spec.Port,
	)
	if err := os.WriteFile(filepath.Join(registryRoot, "manifest"), []byte(installedManifest), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := verifyDesktopEntry(registryRoot, targetRoot, spec); err != nil {
		t.Fatalf("valid installed fnOS layout was rejected: %v", err)
	}
}

func TestParseManifestAcceptsFnpackAlignedFields(t *testing.T) {
	manifest := []byte(
		"appname               = blinko.dkfn\n" +
			"desktop_uidir         = ui\n" +
			"desktop_applaunchname = blinko.dkfn\n" +
			"service_port          = 1111\n" +
			"checksum              = 58783f3327ec57d2d2dc2d470cb8f81c\n",
	)
	fields := parseManifest(manifest)
	if fields["appname"] != "blinko.dkfn" ||
		fields["desktop_applaunchname"] != "blinko.dkfn" ||
		fields["service_port"] != "1111" {
		t.Fatalf("fnpack-aligned manifest fields were not normalized: %#v", fields)
	}
}

func writeValidDesktopFixture(t *testing.T, registryRoot, targetRoot string, spec app.AppSpec) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(targetRoot, "ui", "images"), 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := "appname=" + spec.AppName + "\ndesktop_uidir=ui\ndesktop_applaunchname=" +
		app.DesktopEntryName(spec.AppName) + "\nservice_port=" + strconv.FormatUint(uint64(spec.Port), 10) + "\nctl_stop=true\n"
	if err := os.WriteFile(filepath.Join(registryRoot, "manifest"), []byte(manifest), 0o640); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(
		`{".url":{%q:{"title":%q,"icon":"images/icon_{0}.png","type":%q,"protocol":%q,"port":%q,"url":%q,"allUsers":false,"control":{"accessPerm":"readonly"}}}}`,
		app.DesktopEntryName(spec.AppName), spec.DisplayName, app.NormalizeOpenType(spec.OpenType), spec.Protocol,
		strconv.FormatUint(uint64(spec.Port), 10), spec.Path,
	)
	if err := os.WriteFile(filepath.Join(targetRoot, "ui", "config"), []byte(config), 0o640); err != nil {
		t.Fatal(err)
	}
	writeTestPNG(t, filepath.Join(targetRoot, "ui", "images", "icon_64.png"), 64)
	writeTestPNG(t, filepath.Join(targetRoot, "ui", "images", "icon_256.png"), 256)
}

func TestVerifyDesktopEntryRequiresMatchingEntryAndIcons(t *testing.T) {
	root := t.TempDir()
	spec := app.AppSpec{
		ID: "012345abcdef", AppName: "photos.dkfn", DisplayName: "Photos",
		OpenType: "iframe", Protocol: "http", Port: 8080, Path: "/", Revision: 1,
	}
	configDir := filepath.Join(root, "ui")
	if err := os.MkdirAll(filepath.Join(configDir, "images"), 0o750); err != nil {
		t.Fatal(err)
	}
	config := `{".url":{"photos.dkfn":{"title":"Photos","icon":"images/icon_{0}.png","type":"iframe","protocol":"http","port":"8080","url":"/","allUsers":false,"control":{"accessPerm":"readonly"}}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config"), []byte(config), 0o640); err != nil {
		t.Fatal(err)
	}
	manifest := "appname=photos.dkfn\ndesktop_uidir=ui\ndesktop_applaunchname=photos.dkfn\nservice_port=8080\nctl_stop=true\n"
	if err := os.WriteFile(filepath.Join(root, "manifest"), []byte(manifest), 0o640); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"icon_64.png", "icon_256.png"} {
		size := 64
		if name == "icon_256.png" {
			size = 256
		}
		writeTestPNG(t, filepath.Join(configDir, "images", name), size)
	}
	if err := verifyDesktopEntry(root, root, spec); err != nil {
		t.Fatal(err)
	}
	badConfig := `{".url":{"photos.dkfn":{"title":"Photos","icon":"images/icon_{0}.png","type":"iframe","protocol":"http","port":8080,"path":"/","allUsers":false}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config"), []byte(badConfig), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := verifyDesktopEntry(root, root, spec); err == nil {
		t.Fatal("numeric port and path field were accepted")
	}
	if err := os.WriteFile(filepath.Join(configDir, "config"), []byte(config), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(configDir, "images", "icon_256.png")); err != nil {
		t.Fatal(err)
	}
	if err := verifyDesktopEntry(root, root, spec); err == nil {
		t.Fatal("missing desktop icon accepted")
	}
}

func writeTestPNG(t *testing.T, path string, size int) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			canvas.Set(x, y, color.RGBA{R: 20, G: 40, B: 60, A: 255})
		}
	}
	if err = png.Encode(file, canvas); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHelperRemovesNewShellWhenDesktopEntryValidationFails(t *testing.T) {
	helper, spec, relative, runner := helperFixture(t)
	helper.DesktopEntryVerifier = func(string, string, app.AppSpec) error {
		return errors.New("desktop config is missing")
	}
	if _, err := helper.install(context.Background(), "install", actionRequest{AppName: spec.AppName, SourceRelative: relative}); err == nil || !strings.Contains(err.Error(), "removed the incomplete DockFN shell") {
		t.Fatalf("desktop validation failure was not reported and cleaned up: %v", err)
	}
	if _, exists := runner.registrations[spec.AppName]; exists {
		t.Fatal("incomplete DockFN shell was retained in application center")
	}
	if helper.owned(spec.AppName) {
		t.Fatal("failed install wrote an ownership marker")
	}
	report := filepath.Join(helper.DataDir, "diagnostics", "last-install-failure.json")
	body, readErr := os.ReadFile(report)
	if readErr != nil || !strings.Contains(string(body), spec.AppName) ||
		!strings.Contains(string(body), "desktop config is missing") {
		t.Fatalf("desktop validation diagnostics were not retained: body=%q err=%v", body, readErr)
	}
}

func TestHelperExposesOnlyFixedActions(t *testing.T) {
	helper, _, _, _ := helperFixture(t)
	for _, path := range []string{"/v1/install", "/v1/update", "/v1/remove", "/v1/diagnostics"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		helper.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET %s status=%d, want 405", path, response.Code)
		}
	}
	for _, path := range []string{"/v1/status", "/v1/command", "/v1/observe", "/v1/converge"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		helper.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("POST %s status=%d, want 404", path, response.Code)
		}
	}
}

func TestHelperClearsDiagnosticsThroughFixedEndpoint(t *testing.T) {
	helper, _, _, _ := helperFixture(t)
	if err := os.MkdirAll(helper.LogDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(helper.LogDir, "server.log"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	reportDirectory := filepath.Join(helper.DataDir, "diagnostics")
	if err := os.MkdirAll(reportDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reportDirectory, "last-discovery.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodDelete, "/v1/diagnostics", nil)
	response := httptest.NewRecorder()
	helper.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("DELETE /v1/diagnostics status=%d body=%s", response.Code, response.Body.String())
	}
	if info, err := os.Stat(filepath.Join(helper.LogDir, "server.log")); err != nil || info.Size() != 0 {
		t.Fatalf("server.log was not truncated: info=%#v err=%v", info, err)
	}
	if _, err := os.Stat(filepath.Join(reportDirectory, "last-discovery.json")); !os.IsNotExist(err) {
		t.Fatalf("last discovery report was retained: %v", err)
	}
}
