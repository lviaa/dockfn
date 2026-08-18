package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dockfn/dockfn/internal/app"
)

const settingsFormatVersion = 1

// Settings contains administrator-wide defaults. The concrete entry prefix
// is persisted in each AppSpec, so changing this policy never renames an
// existing application.
type Settings = app.Settings

type settingsDocument struct {
	FormatVersion int      `json:"formatVersion"`
	Settings      Settings `json:"settings"`
}

type SettingsStore struct {
	path string
	mu   sync.RWMutex
	data settingsDocument
}

func OpenSettingsStore(dataDir string) (*SettingsStore, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, errors.New("data directory must be absolute")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	store := &SettingsStore{
		path: filepath.Join(dataDir, "settings.json"),
		data: settingsDocument{
			FormatVersion: settingsFormatVersion,
			Settings:      app.DefaultSettings(),
		},
	}
	body, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(body, &store.data); err != nil {
		return nil, fmt.Errorf("read settings.json: %w", err)
	}
	if store.data.FormatVersion != settingsFormatVersion {
		return nil, fmt.Errorf("unsupported settings.json format version %d", store.data.FormatVersion)
	}
	store.data.Settings = normalizeLoadedSettings(store.data.Settings)
	if err = app.ValidateSettings(store.data.Settings); err != nil {
		return nil, fmt.Errorf("read settings.json: %w", err)
	}
	return store, nil
}

func (s *SettingsStore) Get(context.Context) (Settings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Settings, nil
}

func (s *SettingsStore) Replace(_ context.Context, settings Settings) error {
	settings.EntryPrefixTemplate = strings.TrimSpace(settings.EntryPrefixTemplate)
	settings.DefaultOpenType = app.NormalizeOpenType(settings.DefaultOpenType)
	if err := app.ValidateSettings(settings); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Settings = settings
	return s.saveLocked()
}

func (s *SettingsStore) saveLocked() error {
	body, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.path, append(body, '\n'))
}

func normalizeLoadedSettings(settings Settings) Settings {
	settings.EntryPrefixTemplate = strings.TrimSpace(settings.EntryPrefixTemplate)
	if settings.EntryPrefixTemplate == "" || settings.EntryPrefixTemplate == "d{id}" {
		// Early 0.1.1 test packages stored the automatically suffixed prefix
		// template d{id}. Treat it as the new complete-ID default so those
		// installations can upgrade without failing at startup.
		settings.EntryPrefixTemplate = app.DefaultEntryPrefixTemplate
	}
	settings.DefaultOpenType = app.NormalizeOpenType(settings.DefaultOpenType)
	return settings
}
