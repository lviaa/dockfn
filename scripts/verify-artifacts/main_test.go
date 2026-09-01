package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFnOSPackageSourcesUseCurrentArchitectureContract(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(filepath.Join("..", ".."))
	manifest, err := os.ReadFile(filepath.Join(root, "packaging", "fnos", "common", "manifest.template"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), "\narch=") {
		t.Fatal("top-level manifest retained the deprecated arch field")
	}
	preflight, err := os.ReadFile(filepath.Join(root, "packaging", "fnos", "common", "cmd", "preflight"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(preflight), "/usr/bin/setpriv") {
		t.Fatal("preflight requires setpriv even though no lifecycle script uses it")
	}
}

func TestManifestFieldAcceptsFnpackNormalizedSpacing(t *testing.T) {
	t.Parallel()
	manifest := "appname                    = dockfn\nplatform                   = arm\n"
	if got := manifestField(manifest, "appname"); got != "dockfn" {
		t.Fatalf("manifestField(appname) = %q, want dockfn", got)
	}
	if got := manifestField(manifest, "platform"); got != "arm" {
		t.Fatalf("manifestField(platform) = %q, want arm", got)
	}
}
