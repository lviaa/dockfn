package shellpkg

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/dockfn/dockfn/internal/app"
	"github.com/dockfn/dockfn/internal/iconimage"
)

//go:embed brand/dockfn-badge.png
var dockFNBadge []byte

type Builder struct {
	DataDir    string
	StagingDir string
}

type packageSpec struct {
	Owner string      `json:"owner"`
	App   app.AppSpec `json:"app"`
}

func (b *Builder) Render(_ context.Context, spec app.AppSpec) (app.BuildSource, error) {
	if err := app.Validate(spec); err != nil {
		return app.BuildSource{}, err
	}
	if !filepath.IsAbs(b.StagingDir) || !filepath.IsAbs(b.DataDir) {
		return app.BuildSource{}, errors.New("package paths must be absolute")
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return app.BuildSource{}, err
	}
	operation := spec.ID + "-" + hex.EncodeToString(suffix)
	root := filepath.Join(b.StagingDir, operation, "source")
	if !Within(b.StagingDir, root) {
		return app.BuildSource{}, errors.New("staging path escaped")
	}
	if err := os.MkdirAll(filepath.Join(root, "app", "ui", "images"), 0o750); err != nil {
		return app.BuildSource{}, err
	}
	for _, directory := range []string{"cmd", "config", "wizard"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o750); err != nil {
			return app.BuildSource{}, err
		}
	}
	small, large, err := b.icons(spec)
	if err != nil {
		return app.BuildSource{}, err
	}
	specBody, err := json.MarshalIndent(packageSpec{Owner: "dockfn", App: spec}, "", "  ")
	if err != nil {
		return app.BuildSource{}, err
	}
	files := map[string]fileContent{
		"manifest":                   {Body: manifest(spec), Mode: 0o640},
		"app/ui/config":              {Body: uiConfig(spec), Mode: 0o640},
		"app/ui/images/icon_64.png":  {Body: small, Mode: 0o640},
		"app/ui/images/icon_256.png": {Body: large, Mode: 0o640},
		"ICON.PNG":                   {Body: small, Mode: 0o640},
		"ICON_256.PNG":               {Body: large, Mode: 0o640},
		"spec.json":                  {Body: append(specBody, '\n'), Mode: 0o640},
		"config/privilege":           {Body: []byte("{\"defaults\":{\"run-as\":\"root\"}}\n"), Mode: 0o640},
		"config/resource":            {Body: []byte("{}\n"), Mode: 0o640},
		"cmd/main":                   registrationLifecycle(),
		"cmd/install_init":           executableNoop(),
		"cmd/install_callback":       executableNoop(),
		"cmd/upgrade_init":           executableNoop(),
		"cmd/upgrade_callback":       executableNoop(),
		"cmd/uninstall_init":         executableNoop(),
		"cmd/uninstall_callback":     executableNoop(),
		"cmd/config_init":            executableNoop(),
		"cmd/config_callback":        executableNoop(),
		"cmd/preflight":              executableNoop(),
		"cmd/migrate":                executableNoop(),
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if !Within(root, path) {
			return app.BuildSource{}, errors.New("package member escaped")
		}
		if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return app.BuildSource{}, err
		}
		if err = os.WriteFile(path, content.Body, content.Mode); err != nil {
			return app.BuildSource{}, err
		}
	}
	if err = ValidateDirectory(root, spec.AppName); err != nil {
		return app.BuildSource{}, err
	}
	return app.BuildSource{Directory: root}, nil
}

type fileContent struct {
	Body []byte
	Mode os.FileMode
}

func executableNoop() fileContent {
	return fileContent{Body: []byte("#!/bin/sh\nexit 0\n"), Mode: 0o750}
}

func registrationLifecycle() fileContent {
	return fileContent{Body: []byte(`#!/bin/sh
set -eu

state="${TRIM_PKGVAR:?}/registration.running"
case "${1:-}" in
  start)
    mkdir -p "$(dirname "$state")"
    : >"$state"
    ;;
  stop)
    rm -f "$state"
    ;;
  status)
    if [ -f "$state" ]; then
      exit 0
    fi
    exit 3
    ;;
  *)
    exit 1
    ;;
esac
`), Mode: 0o750}
}

func manifest(spec app.AppSpec) []byte {
	platform, architecture := "x86", "x86_64"
	if runtime.GOARCH == "arm64" {
		platform, architecture = "arm", "aarch64"
	}
	description := manifestValue(spec.Description)
	if description == "" {
		description = manifestValue(spec.DisplayName)
	}
	return []byte(fmt.Sprintf(
		"appname=%s\ndisplay_name=%s\nversion=1.0.%d\ndesc=%s\nplatform=%s\narch=%s\nsource=thirdparty\nmaintainer=DockFN Project\ndistributor=DockFN Project\nos_min_version=1.1.3100\ninstall_type=root\ndesktop_uidir=ui\ndesktop_applaunchname=%s\nservice_port=%d\nctl_stop=true\ncheckport=false\n",
		spec.AppName, manifestValue(spec.DisplayName), spec.Revision, description,
		platform, architecture, app.DesktopEntryName(spec.AppName), spec.Port,
	))
}

func uiConfig(spec app.AppSpec) []byte {
	entry := map[string]any{
		"title":    spec.DisplayName,
		"icon":     "images/icon_{0}.png",
		"type":     app.NormalizeOpenType(spec.OpenType),
		"protocol": spec.Protocol,
		"port":     strconv.FormatUint(uint64(spec.Port), 10),
		"url":      spec.Path,
		"allUsers": spec.AllUsers,
	}
	if !spec.AllUsers {
		entry["control"] = map[string]string{"accessPerm": "readonly"}
	}
	config := map[string]any{
		".url": map[string]any{
			app.DesktopEntryName(spec.AppName): entry,
		},
	}
	body, _ := json.MarshalIndent(config, "", "  ")
	return append(body, '\n')
}

func (b *Builder) icons(spec app.AppSpec) ([]byte, []byte, error) {
	if spec.IconPath == "" {
		small, err := defaultIcon(64)
		if err != nil {
			return nil, nil, err
		}
		large, err := defaultIcon(256)
		if err != nil {
			return nil, nil, err
		}
		badgedSmall, err := withDockFNBadge(small, 64)
		if err != nil {
			return nil, nil, err
		}
		badgedLarge, err := withDockFNBadge(large, 256)
		if err != nil {
			return nil, nil, err
		}
		return badgedSmall, badgedLarge, nil
	}
	smallPath := filepath.Join(b.DataDir, filepath.FromSlash(spec.IconPath))
	iconRoot := filepath.Join(b.DataDir, "icons")
	if !Within(iconRoot, smallPath) || filepath.Base(smallPath) != "ICON.PNG" {
		return nil, nil, errors.New("icon path escaped")
	}
	largePath := filepath.Join(filepath.Dir(smallPath), "ICON_256.PNG")
	small, err := readPNG(smallPath, 64)
	if err != nil {
		return nil, nil, err
	}
	large, err := readPNG(largePath, 256)
	if err != nil {
		return nil, nil, err
	}
	badgedSmall, err := withDockFNBadge(small, 64)
	if err != nil {
		return nil, nil, err
	}
	badgedLarge, err := withDockFNBadge(large, 256)
	if err != nil {
		return nil, nil, err
	}
	return badgedSmall, badgedLarge, nil
}

func withDockFNBadge(base []byte, size int) ([]byte, error) {
	icon, err := png.Decode(bytes.NewReader(base))
	if err != nil {
		return nil, err
	}
	badge, err := png.Decode(bytes.NewReader(dockFNBadge))
	if err != nil {
		return nil, err
	}
	canvas := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(canvas, canvas.Bounds(), icon, icon.Bounds().Min, draw.Src)
	badgeSize := max(18, size*34/100)
	margin := max(2, size*4/100)
	scaled := iconimage.Scale(badge, badgeSize, badgeSize)
	position := image.Pt(size-badgeSize-margin, size-badgeSize-margin)
	draw.Draw(canvas, image.Rectangle{Min: position, Max: position.Add(image.Pt(badgeSize, badgeSize))}, scaled, scaled.Bounds().Min, draw.Over)
	var output bytes.Buffer
	if err = png.Encode(&output, canvas); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func defaultIcon(size int) ([]byte, error) {
	source, err := png.Decode(bytes.NewReader(dockFNBadge))
	if err != nil {
		return nil, err
	}
	if bounds := source.Bounds(); bounds.Dx() == size && bounds.Dy() == size {
		return append([]byte(nil), dockFNBadge...), nil
	}
	var output bytes.Buffer
	if err = png.Encode(&output, iconimage.FitSquare(source, size)); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func readPNG(path string, expected int) ([]byte, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	config, err := png.DecodeConfig(strings.NewReader(string(body)))
	if err != nil || config.Width != expected || config.Height != expected {
		return nil, fmt.Errorf("%s must be a %dx%d PNG", filepath.Base(path), expected, expected)
	}
	return body, nil
}

func manifestValue(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}
