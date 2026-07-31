package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dockfn/dockfn/internal/app"
)

func sampleSpec() app.AppSpec {
	return app.AppSpec{
		ID: "012345abcdef", AppName: "photos.dkfn", DisplayName: "Photos",
		OpenType: "iframe", Protocol: "http", Port: 8080, Path: "/", Revision: 1,
	}
}

func TestFileStoreDefaultsMissingOpenTypeToURL(t *testing.T) {
	data := t.TempDir()
	body := `{"formatVersion":1,"apps":[{"id":"012345abcdef","appName":"photos.dkfn","displayName":"Photos","protocol":"http","port":8080,"path":"/","allUsers":false,"revision":1}]}`
	if err := os.WriteFile(filepath.Join(data, "apps.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFileStore(data)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := store.Get(context.Background(), "012345abcdef")
	if err != nil || spec.OpenType != "url" {
		t.Fatalf("default open type=%q err=%v, want url", spec.OpenType, err)
	}
}

func TestFileStoreWritesAndReloadsAtomically(t *testing.T) {
	data := t.TempDir()
	store, err := OpenFileStore(data)
	if err != nil {
		t.Fatal(err)
	}
	spec := sampleSpec()
	if err = store.Create(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err = store.SetLastError(context.Background(), spec.ID, "last operation failed"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := OpenFileStore(data)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := reloaded.Get(context.Background(), spec.ID)
	if err != nil || actual != spec {
		t.Fatalf("reloaded spec=%#v err=%v", actual, err)
	}
	lastError, _ := reloaded.LastError(context.Background(), spec.ID)
	if lastError != "last operation failed" {
		t.Fatalf("unexpected last error %q", lastError)
	}
	matches, err := filepath.Glob(filepath.Join(data, ".apps.json.tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("atomic write left temporary files: %#v, %v", matches, err)
	}
	body, err := os.ReadFile(filepath.Join(data, "apps.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if json.Unmarshal(body, &document) != nil || int(document["formatVersion"].(float64)) != formatVersion {
		t.Fatalf("invalid atomic JSON document: %s", body)
	}
}

func TestFileStoreAllowsSafeAppNameReplacement(t *testing.T) {
	store, err := OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec := sampleSpec()
	if err = store.Create(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	spec.Revision++
	spec.AppName = "media.dkfn"
	if err = store.Update(context.Background(), spec); err != nil {
		t.Fatalf("safe appName migration was rejected: %v", err)
	}
	stored, err := store.Get(context.Background(), spec.ID)
	if err != nil || stored.AppName != "media.dkfn" {
		t.Fatalf("updated spec=%#v err=%v", stored, err)
	}
}

func TestDiscoveryStoreWritesAndReloadsIgnoredKeys(t *testing.T) {
	data := t.TempDir()
	store, err := OpenDiscoveryStore(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ReplaceIgnored(context.Background(), []string{"docker:demo:8080", "host:panel:12212", "docker:demo:8080"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := OpenDiscoveryStore(data)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := reloaded.ListIgnored(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docker:demo:8080", "host:panel:12212"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("ignored keys=%v, want %v", keys, want)
	}
	body, err := os.ReadFile(filepath.Join(data, "discovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if json.Unmarshal(body, &document) != nil || int(document["formatVersion"].(float64)) != discoveryPreferencesVersion {
		t.Fatalf("invalid discovery document: %s", body)
	}
}
