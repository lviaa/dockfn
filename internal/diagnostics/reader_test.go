package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotReturnsOnlyMeaningfulRecordsAndRedactsSecrets(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "helper.log"), []byte("token=private-value\nhelper started\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "diagnostics"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "diagnostics", "last-discovery.json"), []byte(`{"token":"private-value"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := (Reader{LogDir: directory, DataDir: data}).Snapshot()
	if len(snapshot.Logs) != 1 || snapshot.Logs[0].Name != "runtime.log" || !snapshot.Logs[0].Present {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if strings.Contains(snapshot.Logs[0].Text, "private-value") || !strings.Contains(snapshot.Logs[0].Text, "[REDACTED]") {
		t.Fatalf("secret was exposed: %q", snapshot.Logs[0].Text)
	}
	if len(snapshot.Reports) != 1 || snapshot.Reports[0].Name != "last-discovery.json" ||
		strings.Contains(snapshot.Reports[0].Text, "private-value") {
		t.Fatalf("diagnostic reports were not exposed safely: %#v", snapshot.Reports)
	}
}

func TestSnapshotOmitsEmptyAndLifecycleOnlyLogs(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "lifecycle.log"), []byte("fallback only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "server.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := (Reader{LogDir: directory, DataDir: t.TempDir()}).Snapshot()
	if len(snapshot.Logs) != 0 || len(snapshot.Reports) != 0 {
		t.Fatalf("empty snapshot=%#v", snapshot)
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"logs":[],"reports":[]}` {
		t.Fatalf("empty diagnostic arrays must not become null: %s", body)
	}
}
