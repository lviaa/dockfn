package diagnostics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotReadsKnownLogsAndRedactsSecrets(t *testing.T) {
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
	if len(snapshot.Logs) != 3 || !snapshot.Logs[2].Present {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if strings.Contains(snapshot.Logs[2].Text, "private-value") || !strings.Contains(snapshot.Logs[2].Text, "[REDACTED]") {
		t.Fatalf("secret was exposed: %q", snapshot.Logs[2].Text)
	}
	if len(snapshot.Reports) != 2 || !snapshot.Reports[1].Present ||
		strings.Contains(snapshot.Reports[1].Text, "private-value") {
		t.Fatalf("diagnostic reports were not exposed safely: %#v", snapshot.Reports)
	}
}
