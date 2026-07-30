package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/dockfn/dockfn/internal/app"
)

const formatVersion = 1

type document struct {
	FormatVersion int               `json:"formatVersion"`
	Apps          []app.AppSpec     `json:"apps"`
	LastErrors    map[string]string `json:"lastErrors,omitempty"`
}

// FileStore keeps the one current AppSpec collection in a single atomic JSON
// document. Package and icon files are implementation details outside this
// document.
type FileStore struct {
	path string
	mu   sync.RWMutex
	data document
}

func OpenFileStore(dataDir string) (*FileStore, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, errors.New("data directory must be absolute")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	store := &FileStore{
		path: filepath.Join(dataDir, "apps.json"),
		data: document{FormatVersion: formatVersion, Apps: []app.AppSpec{}, LastErrors: map[string]string{}},
	}
	body, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(body, &store.data); err != nil {
		return nil, fmt.Errorf("read apps.json: %w", err)
	}
	if store.data.FormatVersion != formatVersion {
		return nil, fmt.Errorf("unsupported apps.json format version %d", store.data.FormatVersion)
	}
	if store.data.LastErrors == nil {
		store.data.LastErrors = map[string]string{}
	}
	seenIDs := map[string]bool{}
	seenNames := map[string]bool{}
	for index := range store.data.Apps {
		spec := &store.data.Apps[index]
		spec.OpenType = app.NormalizeOpenType(spec.OpenType)
		if err = app.Validate(*spec); err != nil {
			return nil, fmt.Errorf("invalid stored application %q: %w", spec.ID, err)
		}
		if seenIDs[spec.ID] || seenNames[spec.AppName] {
			return nil, errors.New("apps.json contains duplicate IDs or app names")
		}
		seenIDs[spec.ID], seenNames[spec.AppName] = true, true
	}
	return store, nil
}

func (s *FileStore) List(context.Context) ([]app.AppSpec, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := append([]app.AppSpec(nil), s.data.Apps...)
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (s *FileStore) Get(_ context.Context, id string) (app.AppSpec, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, spec := range s.data.Apps {
		if spec.ID == id {
			return spec, nil
		}
	}
	return app.AppSpec{}, app.ErrNotFound
}

func (s *FileStore) Create(_ context.Context, spec app.AppSpec) error {
	if err := app.Validate(spec); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.data.Apps {
		if existing.ID == spec.ID || existing.AppName == spec.AppName {
			return errors.New("application ID or appName already exists")
		}
	}
	s.data.Apps = append(s.data.Apps, spec)
	return s.saveLocked()
}

func (s *FileStore) Update(_ context.Context, spec app.AppSpec) error {
	if err := app.Validate(spec); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, existing := range s.data.Apps {
		if existing.ID != spec.ID {
			if existing.AppName == spec.AppName {
				return errors.New("application appName already exists")
			}
			continue
		}
		if spec.Revision <= existing.Revision {
			return errors.New("revision must increase")
		}
		s.data.Apps[index] = spec
		return s.saveLocked()
	}
	return app.ErrNotFound
}

func (s *FileStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, spec := range s.data.Apps {
		if spec.ID != id {
			continue
		}
		s.data.Apps = append(s.data.Apps[:index], s.data.Apps[index+1:]...)
		delete(s.data.LastErrors, id)
		return s.saveLocked()
	}
	return app.ErrNotFound
}

func (s *FileStore) LastError(_ context.Context, id string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.LastErrors[id], nil
}

func (s *FileStore) SetLastError(_ context.Context, id, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if message == "" {
		delete(s.data.LastErrors, id)
	} else {
		if len(message) > 1000 {
			message = message[:1000]
		}
		s.data.LastErrors[id] = message
	}
	return s.saveLocked()
}

func (s *FileStore) ReplaceAll(specs []app.AppSpec) error {
	for index := range specs {
		specs[index].OpenType = app.NormalizeOpenType(specs[index].OpenType)
		spec := specs[index]
		if err := app.Validate(spec); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Apps = append([]app.AppSpec(nil), specs...)
	s.data.LastErrors = map[string]string{}
	return s.saveLocked()
}

func (s *FileStore) saveLocked() error {
	sort.Slice(s.data.Apps, func(i, j int) bool { return s.data.Apps[i].ID < s.data.Apps[j].ID })
	body, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicWrite(s.path, body)
}

func atomicWrite(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(body)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return err
	}
	if directory, openErr := os.Open(filepath.Dir(path)); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
