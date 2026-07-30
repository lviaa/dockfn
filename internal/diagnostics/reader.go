package diagnostics

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const tailBytes = 32 << 10

var (
	secretValue     = regexp.MustCompile(`(?i)(password|token|authorization|cookie)\s*([=:])\s*[^\s]+`)
	secretJSONValue = regexp.MustCompile(`(?i)("(?:password|token|authorization|cookie)"\s*:\s*)"[^"]*"`)
)

type Snapshot struct {
	Logs    []Log `json:"logs"`
	Reports []Log `json:"reports"`
}

type Log struct {
	Name    string `json:"name"`
	Text    string `json:"text"`
	Present bool   `json:"present"`
}

type Reader struct {
	LogDir  string
	DataDir string
}

func (r Reader) Snapshot() Snapshot {
	logs := make([]Log, 0, 3)
	for _, name := range []string{"lifecycle.log", "server.log", "helper.log"} {
		text, present := readTail(filepath.Join(r.LogDir, name))
		logs = append(logs, Log{Name: name, Text: redact(text), Present: present})
	}
	reports := make([]Log, 0, 2)
	for _, name := range []string{"last-install-failure.json", "last-discovery.json"} {
		text, present := readTail(filepath.Join(r.DataDir, "diagnostics", name))
		reports = append(reports, Log{Name: name, Text: redact(text), Present: present})
	}
	return Snapshot{Logs: logs, Reports: reports}
}

func readTail(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	start := max(int64(0), info.Size()-tailBytes)
	if _, err = file.Seek(start, io.SeekStart); err != nil {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(file, tailBytes))
	if err != nil {
		return "", false
	}
	if start > 0 {
		if offset := strings.IndexByte(string(body), '\n'); offset >= 0 {
			body = body[offset+1:]
		}
	}
	return string(body), true
}

func redact(value string) string {
	value = secretJSONValue.ReplaceAllString(value, `$1"[REDACTED]"`)
	return secretValue.ReplaceAllString(value, "$1$2[REDACTED]")
}
