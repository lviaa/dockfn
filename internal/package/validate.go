package shellpkg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dockfn/dockfn/internal/app"
)

func ValidateDirectory(root, expectedAppName string) error {
	if !filepath.IsAbs(root) {
		return errors.New("package source must be absolute")
	}
	required := []string{
		"manifest", "app/ui/config", "app/ui/images/icon_64.png", "app/ui/images/icon_256.png",
		"ICON.PNG", "ICON_256.PNG", "spec.json", "config/privilege", "config/resource",
		"cmd/main", "cmd/install_init", "cmd/install_callback", "cmd/upgrade_init",
		"cmd/upgrade_callback", "cmd/uninstall_init", "cmd/uninstall_callback",
		"cmd/config_init", "cmd/config_callback", "cmd/preflight", "cmd/migrate",
	}
	for _, name := range required {
		path := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("missing regular package member %s", name)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("package links are forbidden")
		}
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !Within(root, path) || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("unsafe package path")
		}
		return nil
	}); err != nil {
		return err
	}
	spec, err := ReadSpec(root)
	if err != nil {
		return err
	}
	if spec.AppName != expectedAppName || !app.IsOwnedAppName(expectedAppName) {
		return errors.New("package ownership marker does not match")
	}
	manifest, err := os.ReadFile(filepath.Join(root, "manifest"))
	if err != nil {
		return err
	}
	if !strings.Contains(string(manifest), "\nappname="+expectedAppName+"\n") &&
		!strings.HasPrefix(string(manifest), "appname="+expectedAppName+"\n") {
		return errors.New("manifest appName does not match")
	}
	entryName := app.DesktopEntryName(spec.AppName)
	if !strings.Contains(string(manifest), "\ndesktop_applaunchname="+entryName+"\n") ||
		!strings.Contains(string(manifest), "\nservice_port="+strconv.FormatUint(uint64(spec.Port), 10)+"\n") ||
		!strings.Contains(string(manifest), "\nctl_stop=true\n") {
		return errors.New("manifest desktop entry does not match")
	}
	lifecycle, err := os.ReadFile(filepath.Join(root, "cmd", "main"))
	if err != nil {
		return err
	}
	if !bytes.Contains(lifecycle, []byte("TRIM_PKGVAR")) ||
		!bytes.Contains(lifecycle, []byte("registration.running")) {
		return errors.New("registration lifecycle does not track shell state")
	}
	if err = validateGeneratedDesktopEntry(filepath.Join(root, "app", "ui", "config"), entryName, spec); err != nil {
		return err
	}
	return nil
}

func validateGeneratedDesktopEntry(path, entryName string, spec app.AppSpec) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var config struct {
		URL map[string]json.RawMessage `json:".url"`
	}
	if err = json.Unmarshal(body, &config); err != nil {
		return fmt.Errorf("decode generated desktop config: %w", err)
	}
	raw, exists := config.URL[entryName]
	if !exists {
		return errors.New("generated desktop entry is missing")
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return errors.New("generated desktop entry is invalid")
	}
	if _, exists = fields["path"]; exists {
		return errors.New("generated desktop entry uses path instead of url")
	}
	var entry struct {
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
		Port     string `json:"port"`
		URL      string `json:"url"`
		AllUsers bool   `json:"allUsers"`
	}
	if err = json.Unmarshal(raw, &entry); err != nil {
		return errors.New("generated desktop entry fields are invalid")
	}
	if entry.Type != app.NormalizeOpenType(spec.OpenType) || entry.Protocol != spec.Protocol ||
		entry.Port != strconv.FormatUint(uint64(spec.Port), 10) ||
		entry.URL != spec.Path || entry.AllUsers != spec.AllUsers {
		return errors.New("generated desktop entry does not match the application")
	}
	return nil
}

func ReadSpec(root string) (app.AppSpec, error) {
	var envelope packageSpec
	body, err := os.ReadFile(filepath.Join(root, "spec.json"))
	if err != nil {
		return app.AppSpec{}, err
	}
	if err = json.Unmarshal(body, &envelope); err != nil {
		return app.AppSpec{}, err
	}
	if envelope.Owner != "dockfn" {
		return app.AppSpec{}, errors.New("package owner is not DockFN")
	}
	if err = app.Validate(envelope.App); err != nil {
		return app.AppSpec{}, err
	}
	return envelope.App, nil
}

func Within(root, candidate string) bool {
	rootAbs, rootErr := filepath.Abs(root)
	candidateAbs, candidateErr := filepath.Abs(candidate)
	if rootErr != nil || candidateErr != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, candidateAbs)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
