package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/dockfn/dockfn/internal/app"
)

const discoveryPreferencesVersion = 1
const maxIgnoredCandidateKeys = 500

type discoveryPreferencesDocument struct {
	FormatVersion int      `json:"formatVersion"`
	IgnoredKeys   []string `json:"ignoredKeys"`
}

// DiscoveryStore keeps small, administrator-selected discovery preferences in
// a separate atomic JSON file. Discovery observations themselves remain
// transient; only the explicit ignore decision is persisted.
type DiscoveryStore struct {
	path string
	mu   sync.RWMutex
	data discoveryPreferencesDocument
}

func OpenDiscoveryStore(dataDir string) (*DiscoveryStore, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, errors.New("data directory must be absolute")
	}
	store := &DiscoveryStore{
		path: filepath.Join(dataDir, "discovery.json"),
		data: discoveryPreferencesDocument{FormatVersion: discoveryPreferencesVersion, IgnoredKeys: []string{}},
	}
	body, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(body, &store.data); err != nil {
		return nil, fmt.Errorf("read discovery.json: %w", err)
	}
	if store.data.FormatVersion != discoveryPreferencesVersion {
		return nil, fmt.Errorf("unsupported discovery.json format version %d", store.data.FormatVersion)
	}
	if store.data.IgnoredKeys, err = normalizeIgnoredKeys(store.data.IgnoredKeys); err != nil {
		return nil, fmt.Errorf("read discovery.json: %w", err)
	}
	return store, nil
}

func (s *DiscoveryStore) ListIgnored(context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.data.IgnoredKeys...), nil
}

func (s *DiscoveryStore) ReplaceIgnored(_ context.Context, keys []string) error {
	normalized, err := normalizeIgnoredKeys(keys)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.IgnoredKeys = normalized
	return s.saveLocked()
}

func (s *DiscoveryStore) saveLocked() error {
	body, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.path, append(body, '\n'))
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func normalizeIgnoredKeys(keys []string) ([]string, error) {
	if len(keys) > maxIgnoredCandidateKeys {
		return nil, &app.ValidationError{Fields: []app.FieldError{{
			Field: "keys", Message: fmt.Sprintf("cannot contain more than %d items", maxIgnoredCandidateKeys),
		}}}
	}
	seen := make(map[string]struct{}, len(keys))
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || len(key) > 512 || hasControl(key) {
			return nil, &app.ValidationError{Fields: []app.FieldError{{
				Field: "keys", Message: "must contain 1 to 512 printable characters",
			}}}
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	sort.Strings(result)
	return result, nil
}
