package app

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateAppSpec(t *testing.T) {
	t.Parallel()
	valid := AppSpec{
		ID: "012345abcdef", AppName: "dkfn.photos", EntryID: "photos", DisplayName: "Photos",
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
		{"appName", func(spec *AppSpec) { spec.AppName = "Other/App" }, "appName"},
		{"entryId", func(spec *AppSpec) { spec.EntryID = "1photos" }, "entryId"},
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

func TestResolveIdentityUsesFullTemplateAndStableFallback(t *testing.T) {
	t.Parallel()
	first, err := ResolveIdentity("012345abcdef", "dkfn.{id}", "", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveIdentity("012345abcdef", "dkfn.{id}", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.AppName != "dkfn.d012345abcdef" || first.EntryID != "d012345abcdef" || second != first {
		t.Fatalf("unexpected stable appName %q / %q", first, second)
	}
	if len(first.AppName) > 63 || first.AppName[0] < 'a' || first.AppName[0] > 'z' {
		t.Fatalf("generated fnOS appName is not install-safe: %q", first)
	}
}

func TestResolveIdentityUsesRequestedEntryID(t *testing.T) {
	t.Parallel()
	got, err := ResolveIdentity("012345abcdef", "{id}.dkfn", " blinko-notes ", "Blinko")
	if err != nil {
		t.Fatal(err)
	}
	if got.AppName != "blinko-notes.dkfn" || got.EntryID != "blinko-notes" {
		t.Fatalf("custom appName=%q", got)
	}
	for _, prefix := range []string{"Blinko", "-bad", "bad.", "bad/name", "1panel2", strings.Repeat("a", 28), strings.Repeat("2", 27)} {
		if _, err = ResolveIdentity("012345abcdef", "dkfn.{id}", prefix, ""); err == nil {
			t.Fatalf("unsafe entry prefix %q was accepted", prefix)
		}
	}
}

func TestEntryPrefixTemplateDescribesTheCompleteFnOSID(t *testing.T) {
	t.Parallel()
	for _, template := range []string{"dkfn.{id}", "{id}.dkfn", "app-{id}", "fn.{id}.web"} {
		if err := ValidateEntryPrefixTemplate(template); err != nil {
			t.Fatalf("safe template %q rejected: %v", template, err)
		}
	}
	for _, template := range []string{"{id}", "D.{id}", "app", "1.{id}", "app.{id}."} {
		if err := ValidateEntryPrefixTemplate(template); err == nil {
			t.Fatalf("unsafe template %q accepted", template)
		}
	}
	got, err := RenderAppNameTemplate("{id}.dkfn", "blinko")
	if err != nil || got != "blinko.dkfn" {
		t.Fatalf("rendered prefix=%q err=%v", got, err)
	}
}

func TestSuggestEntryIDNormalizesChineseEnglishAndSymbols(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"飞牛应用坞":               "fei-niu-ying-yong-wu",
		"Blinko Notes":        "blinko-notes",
		"1Panel 2 / 管理面板":     "app-1panel-2-guan-li-mian",
		"  Hello___WORLD!!  ": "hello-world",
		"🎵":                   "",
	}
	for input, want := range tests {
		if got := SuggestEntryID(input); got != want {
			t.Fatalf("SuggestEntryID(%q)=%q want %q", input, got, want)
		}
	}
}

func TestDefaultSettingsAreValid(t *testing.T) {
	t.Parallel()
	settings := DefaultSettings()
	if err := ValidateSettings(settings); err != nil {
		t.Fatal(err)
	}
	if settings.EntryPrefixTemplate != "dkfn.{id}" || settings.DefaultOpenType != "url" ||
		settings.DefaultAllUsers || !settings.AutoScanOnCreate || !settings.ShowDockFNBadge {
		t.Fatalf("unexpected defaults: %#v", settings)
	}
}

func TestOwnedAppNameRejectsExternalNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"dkfn.d012345abcdef", "blinko-notes.dkfn", "app-blinko"} {
		if !IsOwnedAppName(name) {
			t.Fatalf("expected %s to be manageable", name)
		}
	}
	for _, name := range []string{"012345abcdef.dkfn", "Bad.App", "bad/name", "app..demo", strings.Repeat("a", 64)} {
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
