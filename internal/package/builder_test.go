package shellpkg

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockfn/dockfn/internal/app"
)

func packageTestSpec() app.AppSpec {
	return app.AppSpec{
		ID: "012345abcdef", AppName: "photos.dkfn", DisplayName: "Home Photos",
		Description: "A safe description", OpenType: "iframe", Protocol: "https", Port: 8443,
		Path: "/photos/", AllUsers: true, Revision: 4,
	}
}

func TestRenderRegistrationShellSupportsURLMode(t *testing.T) {
	data := t.TempDir()
	builder := &Builder{DataDir: data, StagingDir: filepath.Join(data, "staging")}
	spec := packageTestSpec()
	spec.OpenType = "url"
	source, err := builder.Render(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(source.Directory, "app", "ui", "config"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]map[string]map[string]any
	if err = json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	if entry := config[".url"][spec.AppName]; entry["type"] != "url" {
		t.Fatalf("URL mode was not written to fnOS UI config: %#v", entry)
	}
}

func TestRenderNewIdentityUsesAppNameAsDesktopEntry(t *testing.T) {
	data := t.TempDir()
	builder := &Builder{DataDir: data, StagingDir: filepath.Join(data, "staging")}
	spec := packageTestSpec()
	spec.AppName = "blinko.dkfn"
	source, err := builder.Render(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(source.Directory, "manifest"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "desktop_applaunchname=blinko.dkfn\n") ||
		strings.Contains(string(manifest), "blinko.dkfn.main") {
		t.Fatalf("new identity did not use the appName as its entry ID:\n%s", manifest)
	}
	body, err := os.ReadFile(filepath.Join(source.Directory, "app", "ui", "config"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]map[string]map[string]any
	if err = json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	if _, exists := config[".url"]["blinko.dkfn"]; !exists {
		t.Fatalf("new desktop entry is missing: %s", body)
	}
}

func TestManifestFallsBackToDisplayNameWhenDescriptionIsEmpty(t *testing.T) {
	spec := packageTestSpec()
	spec.DisplayName = "Blinko"
	spec.Description = ""
	body := string(manifest(spec))
	if !strings.Contains(body, "\ndesc=Blinko\n") {
		t.Fatalf("empty product description produced an invalid fnOS manifest:\n%s", body)
	}
}

func TestRegistrationShellIconsContainTheDockFNBadge(t *testing.T) {
	base, err := defaultIcon(64)
	if err != nil {
		t.Fatal(err)
	}
	badged, err := withDockFNBadge(base, 64)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(base, badged) {
		t.Fatal("registration icon did not receive the DockFN badge")
	}
}

func TestEmbeddedDockFNBadgeUsesHighResolutionSource(t *testing.T) {
	config, err := png.DecodeConfig(bytes.NewReader(dockFNBadge))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width < 256 || config.Height < 256 {
		t.Fatalf("badge source is too small for the 256px shell icon: %dx%d", config.Width, config.Height)
	}
}

func TestDefaultRegistrationIconUsesDockFNBrandIcon(t *testing.T) {
	actualBody, err := defaultIcon(256)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := png.Decode(bytes.NewReader(actualBody))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := png.Decode(bytes.NewReader(dockFNBadge))
	if err != nil {
		t.Fatal(err)
	}
	if actual.Bounds() != expected.Bounds() {
		t.Fatalf("default icon bounds=%v, want %v", actual.Bounds(), expected.Bounds())
	}
	for y := actual.Bounds().Min.Y; y < actual.Bounds().Max.Y; y++ {
		for x := actual.Bounds().Min.X; x < actual.Bounds().Max.X; x++ {
			ar, ag, ab, aa := actual.At(x, y).RGBA()
			er, eg, eb, ea := expected.At(x, y).RGBA()
			if ar != er || ag != eg || ab != eb || aa != ea {
				t.Fatalf("default icon differs from the previewed DockFN icon at %d,%d", x, y)
			}
		}
	}
}

func TestRenderRegistrationShell(t *testing.T) {
	data := t.TempDir()
	builder := &Builder{DataDir: data, StagingDir: filepath.Join(data, "staging")}
	source, err := builder.Render(context.Background(), packageTestSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateDirectory(source.Directory, "photos.dkfn"); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(source.Directory, "manifest"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"appname=photos.dkfn", "display_name=Home Photos",
		"version=1.0.4", "desktop_applaunchname=photos.dkfn",
		"service_port=8443", "ctl_stop=true", "checkport=false",
		"maintainer=lviaa", "maintainer_url=https://github.com/lviaa/dockfn",
		"distributor=lviaa", "distributor_url=https://github.com/lviaa/dockfn/releases",
	} {
		if !strings.Contains(string(manifest), expected) {
			t.Fatalf("manifest missing %q:\n%s", expected, manifest)
		}
	}
	lifecycle, err := os.ReadFile(filepath.Join(source.Directory, "cmd", "main"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lifecycle), "registration.running") ||
		!strings.Contains(string(lifecycle), "TRIM_PKGVAR") ||
		strings.Contains(string(lifecycle), "docker") {
		t.Fatalf("registration shell lifecycle has unsafe or incomplete semantics:\n%s", lifecycle)
	}
	body, err := os.ReadFile(filepath.Join(source.Directory, "app", "ui", "config"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]map[string]map[string]any
	if err = json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	entry := config[".url"]["photos.dkfn"]
	if entry["type"] != "iframe" || entry["protocol"] != "https" || entry["port"] != "8443" ||
		entry["url"] != "/photos/" || entry["allUsers"] != true {
		t.Fatalf("unexpected UI entry: %#v", entry)
	}
	if _, exists := entry["path"]; exists {
		t.Fatalf("fnOS UI entry must use url rather than path: %#v", entry)
	}
	for name, size := range map[string]int64{"ICON.PNG": 1, "ICON_256.PNG": 1} {
		info, statErr := os.Stat(filepath.Join(source.Directory, name))
		if statErr != nil || info.Size() < size {
			t.Fatalf("missing generated %s: %v", name, statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(source.Directory, "wizard", "uninstall")); !os.IsNotExist(statErr) {
		t.Fatalf("generated shells must not contain an empty uninstall wizard: %v", statErr)
	}
}

func TestValidateDirectoryRejectsOwnershipMismatch(t *testing.T) {
	data := t.TempDir()
	builder := &Builder{DataDir: data, StagingDir: filepath.Join(data, "staging")}
	source, err := builder.Render(context.Background(), packageTestSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateDirectory(source.Directory, "other.dkfn"); err == nil {
		t.Fatal("expected ownership mismatch")
	}
}

func TestValidateDirectoryRejectsNonStringPortAndPathField(t *testing.T) {
	data := t.TempDir()
	builder := &Builder{DataDir: data, StagingDir: filepath.Join(data, "staging")}
	source, err := builder.Render(context.Background(), packageTestSpec())
	if err != nil {
		t.Fatal(err)
	}
	invalid := `{".url":{"photos.dkfn":{"title":"Home Photos","icon":"images/icon_{0}.png","type":"iframe","protocol":"https","port":8443,"path":"/photos/","allUsers":true}}}`
	if err = os.WriteFile(filepath.Join(source.Directory, "app", "ui", "config"), []byte(invalid), 0o640); err != nil {
		t.Fatal(err)
	}
	if err = ValidateDirectory(source.Directory, "photos.dkfn"); err == nil {
		t.Fatal("path field and numeric port were accepted")
	}
}

func TestWithinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if !Within(root, filepath.Join(root, "child", "file")) {
		t.Fatal("safe child rejected")
	}
	if Within(root, filepath.Join(root, "..", "outside")) {
		t.Fatal("traversal path accepted")
	}
}
