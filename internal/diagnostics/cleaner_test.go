package diagnostics

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanerClearsOnlyKnownDockFNDiagnostics(t *testing.T) {
	root := t.TempDir()
	logDirectory := filepath.Join(root, "logs")
	dataDirectory := filepath.Join(root, "data")
	diagnosticDirectory := filepath.Join(dataDirectory, "diagnostics")
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(diagnosticDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range knownLogs {
		if err := os.WriteFile(filepath.Join(logDirectory, name), []byte("historical log\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range knownReports {
		if err := os.WriteFile(filepath.Join(diagnosticDirectory, name), []byte(`{"historical":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unrelatedLog := filepath.Join(logDirectory, "target-service.log")
	unrelatedReport := filepath.Join(diagnosticDirectory, "operator-note.json")
	if err := os.WriteFile(unrelatedLog, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelatedReport, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := (Cleaner{LogDir: logDirectory, DataDir: dataDirectory}).Clear(); err != nil {
		t.Fatal(err)
	}

	for _, name := range knownLogs {
		info, err := os.Stat(filepath.Join(logDirectory, name))
		if err != nil || info.Size() != 0 {
			t.Fatalf("%s was not retained as an empty log: info=%#v err=%v", name, info, err)
		}
	}
	for _, name := range knownReports {
		if _, err := os.Stat(filepath.Join(diagnosticDirectory, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was not removed: %v", name, err)
		}
	}
	for _, path := range []string{unrelatedLog, unrelatedReport} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "preserve" {
			t.Fatalf("unrelated file %s changed: body=%q err=%v", path, body, err)
		}
	}
}

func TestCleanerRefusesNonRegularLogTargets(t *testing.T) {
	logDirectory := t.TempDir()
	dataDirectory := t.TempDir()
	if err := os.Mkdir(filepath.Join(logDirectory, "helper.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (Cleaner{LogDir: logDirectory, DataDir: dataDirectory}).Clear(); err == nil {
		t.Fatal("Cleaner accepted a non-regular helper.log")
	}
}
