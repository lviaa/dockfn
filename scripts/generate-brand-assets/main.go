package main

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"path/filepath"

	"github.com/dockfn/dockfn/internal/iconimage"
)

func main() {
	sourcePath := filepath.FromSlash("assets/ICON_256.PNG")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		panic(fmt.Errorf("read canonical DockFN icon: %w", err))
	}
	decoded, err := png.Decode(bytes.NewReader(source))
	if err != nil {
		panic(fmt.Errorf("decode canonical DockFN icon: %w", err))
	}
	if bounds := decoded.Bounds(); bounds.Dx() != 256 || bounds.Dy() != 256 {
		panic(fmt.Errorf("canonical DockFN icon must be 256x256, got %dx%d", bounds.Dx(), bounds.Dy()))
	}

	var small bytes.Buffer
	if err = png.Encode(&small, iconimage.FitSquare(decoded, 64)); err != nil {
		panic(fmt.Errorf("encode 64px DockFN icon: %w", err))
	}
	outputs := map[string][]byte{
		filepath.FromSlash("assets/ICON.PNG"):                         small.Bytes(),
		filepath.FromSlash("internal/package/brand/dockfn-badge.png"): source,
		filepath.FromSlash("web/src/assets/dockfn-logo.png"):          source,
	}
	for path, body := range outputs {
		if err = writeIfChanged(path, body); err != nil {
			panic(err)
		}
	}
}

func writeIfChanged(path string, body []byte) error {
	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, body) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	if err = os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
