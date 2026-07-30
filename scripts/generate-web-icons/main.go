package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var iconReference = regexp.MustCompile(`solar:([a-z0-9-]+)`)

type collection struct {
	Prefix string                     `json:"prefix"`
	Icons  map[string]json.RawMessage `json:"icons"`
	Width  int                        `json:"width,omitempty"`
	Height int                        `json:"height,omitempty"`
}

func main() {
	source := filepath.FromSlash("web/node_modules/@iconify-json/solar/icons.json")
	output := filepath.FromSlash("web/src/solar-icons.json")
	if err := generate(source, filepath.FromSlash("web/src"), output); err != nil {
		panic(err)
	}
}

func generate(source, sourceRoot, output string) error {
	body, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read Solar icon collection: %w", err)
	}
	var full collection
	if err = json.Unmarshal(body, &full); err != nil {
		return fmt.Errorf("decode Solar icon collection: %w", err)
	}
	names, err := referencedIcons(sourceRoot, output)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return errors.New("no Solar icon references found")
	}
	minimal := collection{
		Prefix: full.Prefix,
		Icons:  make(map[string]json.RawMessage, len(names)),
		Width:  full.Width,
		Height: full.Height,
	}
	for _, name := range names {
		icon, exists := full.Icons[name]
		if !exists {
			return fmt.Errorf("Solar icon %q does not exist", name)
		}
		minimal.Icons[name] = icon
	}
	generated, err := json.MarshalIndent(minimal, "", "  ")
	if err != nil {
		return err
	}
	generated = append(generated, '\n')
	if current, readErr := os.ReadFile(output); readErr == nil && bytes.Equal(current, generated) {
		return nil
	}
	return os.WriteFile(output, generated, 0o644)
}

func referencedIcons(root, output string) ([]string, error) {
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || path == output {
			return walkErr
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range iconReference.FindAllSubmatch(body, -1) {
			seen[string(match[1])] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
