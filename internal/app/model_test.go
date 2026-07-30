package app

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateAppSpec(t *testing.T) {
	t.Parallel()
	valid := AppSpec{
		ID: "012345abcdef", AppName: "photos.dkfn", DisplayName: "Photos",
		OpenType: "iframe", Protocol: "http", Port: 8080, Path: "/", Revision: 1,
	}
	if err := Validate(valid); err != nil {
		t.Fatalf("valid AppSpec rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*AppSpec)
		field  string
	}{
		{"id", func(spec *AppSpec) { spec.ID = "../escape" }, "id"},
		{"appName", func(spec *AppSpec) { spec.AppName = "other.app" }, "appName"},
		{"openType", func(spec *AppSpec) { spec.OpenType = "native" }, "openType"},
		{"protocol", func(spec *AppSpec) { spec.Protocol = "ftp" }, "protocol"},
		{"origin source", func(spec *AppSpec) { spec.Origin = &Origin{Source: "remote"} }, "origin.source"},
		{"port", func(spec *AppSpec) { spec.Port = 0 }, "port"},
		{"relative path", func(spec *AppSpec) { spec.Path = "admin" }, "path"},
		{"traversal path", func(spec *AppSpec) { spec.Path = "/a/%2e%2e/b" }, "path"},
		{"icon escape", func(spec *AppSpec) { spec.IconPath = "icons/../../secret" }, "iconPath"},
		{"revision", func(spec *AppSpec) { spec.Revision = 0 }, "revision"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			var validation *ValidationError
			if err := Validate(candidate); !errors.As(err, &validation) {
				t.Fatalf("wanted ValidationError, got %v", err)
			}
			found := false
			for _, field := range validation.Fields {
				found = found || field.Field == test.field
			}
			if !found {
				t.Fatalf("wanted field %s in %#v", test.field, validation.Fields)
			}
		})
	}
}

func TestNormalizeOpenTypeDefaultsToURL(t *testing.T) {
	t.Parallel()
	if actual := NormalizeOpenType(""); actual != "url" {
		t.Fatalf("default open type=%q, want url", actual)
	}
	if actual := NormalizeOpenType(" URL "); actual != "url" {
		t.Fatalf("normalized open type=%q, want url", actual)
	}
}

func TestNewAppNameIsStable(t *testing.T) {
	t.Parallel()
	first, err := NewAppName("012345abcdef")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewAppName("012345abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if first != "d012345abcdef.dkfn" || second != first {
		t.Fatalf("unexpected stable appName %q / %q", first, second)
	}
	if len(first) > 32 || first[0] < 'a' || first[0] > 'z' {
		t.Fatalf("generated fnOS appName is not install-safe: %q", first)
	}
}

func TestNewAppNameUsesSafeOptionalEntryPrefix(t *testing.T) {
	t.Parallel()
	got, err := NewAppName("012345abcdef", " blinko-notes ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "blinko-notes.dkfn" {
		t.Fatalf("custom appName=%q", got)
	}
	for _, prefix := range []string{"Blinko", "-bad", "bad.", "bad/name", "1panel2", strings.Repeat("a", 28), strings.Repeat("2", 27)} {
		if _, err = NewAppName("012345abcdef", prefix); err == nil {
			t.Fatalf("unsafe entry prefix %q was accepted", prefix)
		}
	}
}

func TestNewAppNameRejectsNumericRequestForFnOSSafety(t *testing.T) {
	t.Parallel()
	if _, err := NewAppName("012345abcdef", "1panel2"); err == nil {
		t.Fatal("numeric custom appName was accepted")
	}
}

func TestOwnedAppNameRejectsExternalNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"d012345abcdef.dkfn", "blinko-notes.dkfn"} {
		if !IsOwnedAppName(name) {
			t.Fatalf("expected %s to be manageable", name)
		}
	}
	for _, name := range []string{"012345abcdef.dkfn", "watchcow.demo", "other.app", "dockfn.legacy"} {
		if IsOwnedAppName(name) {
			t.Fatalf("expected %s to be rejected", name)
		}
	}
}

func TestDesktopEntryNameUsesAppNameDirectly(t *testing.T) {
	t.Parallel()
	if got := DesktopEntryName("blinko.dkfn"); got != "blinko.dkfn" {
		t.Fatalf("desktop entry=%q", got)
	}
	if got := EntryPrefix("blinko.dkfn"); got != "blinko" {
		t.Fatalf("entry prefix=%q", got)
	}
}
