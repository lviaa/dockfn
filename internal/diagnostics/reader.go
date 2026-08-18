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
	logs := runtimeLog(r.LogDir)
	reports := make([]Log, 0, 2)
	for _, name := range []string{"last-discovery.json", "last-install-failure.json"} {
		text, present := readTail(filepath.Join(r.DataDir, "diagnostics", name))
		if present && strings.TrimSpace(text) != "" {
			reports = append(reports, Log{Name: name, Text: redact(text), Present: true})
		}
	}
	return Snapshot{Logs: logs, Reports: reports}
}

// runtimeLog presents the useful runtime output as one on-demand record. The
// lifecycle log is deliberately excluded: fnOS normally owns it through its
// temporary lifecycle log file, so it is not a dependable application record.
func runtimeLog(directory string) []Log {
	sections := make([]string, 0, 2)
	for _, source := range []struct {
		name  string
		title string
	}{
		{name: "server.log", title: "管理服务"},
		{name: "helper.log", title: "权限助手"},
	} {
		text, present := readTail(filepath.Join(directory, source.name))
		if !present || strings.TrimSpace(text) == "" {
			continue
		}
		sections = append(sections, "["+source.title+"]\n"+text)
	}
	if len(sections) == 0 {
		return make([]Log, 0)
	}
	return []Log{{Name: "runtime.log", Text: redact(strings.Join(sections, "\n\n")), Present: true}}
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
