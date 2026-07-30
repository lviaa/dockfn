package app

import (
	"errors"
	"fmt"
	"net/url"
	pathpkg "path"
	"regexp"
	"strings"
)

// AppSpec is the single persistent product entity. Discovery observations stay
// transient; only the administrator-selected origin snapshot is retained for
// display and search.
type AppSpec struct {
	ID          string  `json:"id"`
	AppName     string  `json:"appName"`
	DisplayName string  `json:"displayName"`
	Description string  `json:"description,omitempty"`
	IconPath    string  `json:"iconPath,omitempty"`
	Origin      *Origin `json:"origin,omitempty"`
	OpenType    string  `json:"openType"`
	Protocol    string  `json:"protocol"`
	Port        uint16  `json:"port"`
	Path        string  `json:"path"`
	AllUsers    bool    `json:"allUsers"`
	Revision    uint32  `json:"revision"`
}

type Input struct {
	DisplayName string  `json:"displayName"`
	Description string  `json:"description,omitempty"`
	EntryPrefix string  `json:"entryPrefix,omitempty"`
	IconBase64  *string `json:"iconBase64,omitempty"`
	IconURI     *string `json:"iconUri,omitempty"`
	Origin      *Origin `json:"origin,omitempty"`
	OpenType    string  `json:"openType,omitempty"`
	Protocol    string  `json:"protocol"`
	Port        uint16  `json:"port"`
	Path        string  `json:"path"`
	AllUsers    bool    `json:"allUsers"`
}

// Origin is an immutable snapshot of the source selected during creation. It
// is informational and never grants DockFN control over a process or container.
type Origin struct {
	Source       string `json:"source"`
	SourceDetail string `json:"sourceDetail,omitempty"`
	Description  string `json:"description,omitempty"`
	NetworkMode  string `json:"networkMode,omitempty"`
	PID          int    `json:"pid,omitempty"`
	WatchCow     bool   `json:"watchCow,omitempty"`
}

type Status struct {
	Registration string `json:"registration"`
	Target       string `json:"target"`
	LastError    string `json:"lastError,omitempty"`
}

type View struct {
	AppSpec
	Status      Status `json:"status"`
	IconDataURL string `json:"iconDataUrl,omitempty"`
}

type OperationResult struct {
	App  View   `json:"app"`
	Code string `json:"code"`
}

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	if len(e.Fields) == 0 {
		return "validation failed"
	}
	return e.Fields[0].Field + ": " + e.Fields[0].Message
}

var (
	idPattern            = regexp.MustCompile(`^[a-f0-9]{12}$`)
	entryPrefixPattern   = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,25}[a-z0-9])?$`)
	domainAppNamePattern = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,25}[a-z0-9])?\.dkfn$`)
)

func NewAppName(id string, requested ...string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", errors.New("invalid application ID")
	}
	if len(requested) > 1 {
		return "", errors.New("only one entry prefix may be provided")
	}
	// Keep the immutable internal ID intact and add a stable letter only to the
	// automatically generated launch identity.
	prefix := "d" + id
	if len(requested) == 1 && strings.TrimSpace(requested[0]) != "" {
		prefix = strings.TrimSpace(requested[0])
		if !entryPrefixPattern.MatchString(prefix) {
			return "", errors.New("must contain 1 to 27 lowercase letters, numbers, or internal hyphens and start with a letter")
		}
	}
	return prefix + ".dkfn", nil
}

func Validate(spec AppSpec) error {
	fields := make([]FieldError, 0)
	if !idPattern.MatchString(spec.ID) {
		fields = append(fields, FieldError{Field: "id", Message: "must be 12 lowercase hexadecimal characters"})
	}
	if !IsOwnedAppName(spec.AppName) || strings.Contains(spec.AppName, "..") {
		fields = append(fields, FieldError{Field: "appName", Message: "must use a safe DockFN identifier"})
	}
	name := strings.TrimSpace(spec.DisplayName)
	if name == "" || len([]rune(name)) > 80 || hasControl(name) {
		fields = append(fields, FieldError{Field: "displayName", Message: "must contain 1 to 80 printable characters"})
	}
	if len([]rune(spec.Description)) > 500 || hasControlExceptNewline(spec.Description) {
		fields = append(fields, FieldError{Field: "description", Message: "must contain at most 500 printable characters"})
	}
	if origin := spec.Origin; origin != nil {
		if origin.Source != "manual" && origin.Source != "docker" && origin.Source != "host" {
			fields = append(fields, FieldError{Field: "origin.source", Message: "must be manual, docker, or host"})
		}
		if len([]rune(origin.SourceDetail)) > 200 || hasControl(origin.SourceDetail) {
			fields = append(fields, FieldError{Field: "origin.sourceDetail", Message: "must contain at most 200 printable characters"})
		}
		if len([]rune(origin.Description)) > 500 || hasControl(origin.Description) {
			fields = append(fields, FieldError{Field: "origin.description", Message: "must contain at most 500 printable characters"})
		}
		if len([]rune(origin.NetworkMode)) > 200 || hasControl(origin.NetworkMode) {
			fields = append(fields, FieldError{Field: "origin.networkMode", Message: "must contain at most 200 printable characters"})
		}
		if origin.PID < 0 {
			fields = append(fields, FieldError{Field: "origin.pid", Message: "must be zero or a positive process ID"})
		}
	}
	if openType := NormalizeOpenType(spec.OpenType); openType != "iframe" && openType != "url" {
		fields = append(fields, FieldError{Field: "openType", Message: "must be iframe or url"})
	}
	if spec.Protocol != "http" && spec.Protocol != "https" {
		fields = append(fields, FieldError{Field: "protocol", Message: "must be http or https"})
	}
	if spec.Port == 0 {
		fields = append(fields, FieldError{Field: "port", Message: "must be between 1 and 65535"})
	}
	if err := validatePath(spec.Path); err != nil {
		fields = append(fields, FieldError{Field: "path", Message: err.Error()})
	}
	if spec.IconPath != "" {
		clean := pathpkg.Clean(spec.IconPath)
		if clean != spec.IconPath || strings.HasPrefix(clean, "/") || !strings.HasPrefix(clean, "icons/") || strings.Contains(clean, `\`) {
			fields = append(fields, FieldError{Field: "iconPath", Message: "must stay inside the icons directory"})
		}
	}
	if spec.Revision == 0 {
		fields = append(fields, FieldError{Field: "revision", Message: "must be at least 1"})
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func NormalizeOpenType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "url"
	}
	return value
}

func validatePath(value string) error {
	if value == "" || !strings.HasPrefix(value, "/") {
		return errors.New("must start with /")
	}
	if len(value) > 512 || strings.ContainsAny(value, `\?#`) || hasControl(value) {
		return errors.New("contains unsupported characters")
	}
	if strings.Contains(value, "//") {
		return errors.New("must not contain empty path segments")
	}
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return errors.New("contains invalid escaping")
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "." || segment == ".." {
			return errors.New("must not contain . or .. segments")
		}
	}
	clean := pathpkg.Clean(value)
	if value != "/" && strings.TrimSuffix(value, "/") != clean {
		return fmt.Errorf("must be normalized as %s", clean)
	}
	return nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func hasControlExceptNewline(value string) bool {
	for _, r := range value {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return true
		}
	}
	return false
}

func IsOwnedAppName(value string) bool {
	return domainAppNamePattern.MatchString(value)
}

// DesktopEntryName centralizes the fnOS identity rule. The entry uses appName
// directly; fnOS remains responsible for any external URL derived from it.
func DesktopEntryName(appName string) string {
	return appName
}

func EntryPrefix(appName string) string {
	if domainAppNamePattern.MatchString(appName) {
		return strings.TrimSuffix(appName, ".dkfn")
	}
	return ""
}
