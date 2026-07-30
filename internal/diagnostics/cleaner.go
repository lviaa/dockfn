package diagnostics

import (
	"errors"
	"fmt"
	"os"
)

var (
	knownLogs    = []string{"lifecycle.log", "server.log", "helper.log"}
	knownReports = []string{"last-install-failure.json", "last-discovery.json"}
)

// Cleaner clears only DockFN-owned diagnostic history. Logs are truncated so
// processes that already hold append descriptors can continue writing to the
// same files; reports are removed because they are point-in-time snapshots.
type Cleaner struct {
	LogDir  string
	DataDir string
}

func (c Cleaner) Clear() error {
	var failures []error
	if err := truncateKnownLogs(c.LogDir); err != nil {
		failures = append(failures, err)
	}
	if err := removeKnownReports(c.DataDir); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func truncateKnownLogs(directory string) error {
	root, err := os.OpenRoot(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open DockFN log directory: %w", err)
	}
	defer func() { _ = root.Close() }()

	var failures []error
	for _, name := range knownLogs {
		info, statErr := root.Lstat(name)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			failures = append(failures, fmt.Errorf("inspect %s: %w", name, statErr))
			continue
		}
		if !info.Mode().IsRegular() {
			failures = append(failures, fmt.Errorf("refuse to truncate non-regular DockFN log %s", name))
			continue
		}
		file, openErr := root.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0)
		if openErr != nil {
			failures = append(failures, fmt.Errorf("truncate %s: %w", name, openErr))
			continue
		}
		if closeErr := file.Close(); closeErr != nil {
			failures = append(failures, fmt.Errorf("close %s: %w", name, closeErr))
		}
	}
	return errors.Join(failures...)
}

func removeKnownReports(dataDirectory string) error {
	root, err := os.OpenRoot(dataDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open DockFN data directory: %w", err)
	}
	defer func() { _ = root.Close() }()

	var failures []error
	for _, name := range knownReports {
		if removeErr := root.Remove("diagnostics/" + name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			failures = append(failures, fmt.Errorf("remove %s: %w", name, removeErr))
		}
	}
	return errors.Join(failures...)
}
