package app

import (
	"errors"
	"strings"
	"unicode"

	pinyin "github.com/mozillazg/go-pinyin"
)

const DefaultEntryPrefixTemplate = "dkfn.{id}"

type Identity struct {
	EntryID string `json:"entryId"`
	AppName string `json:"appName"`
}

// ResolveIdentity is the single identity policy seam. The immutable internal
// ID is only a fallback; the fnOS identity is always rendered from the global
// full-name template and a validated, human-readable entry ID.
func ResolveIdentity(internalID, template, requestedEntryID, displayName string) (Identity, error) {
	if !idPattern.MatchString(internalID) {
		return Identity{}, errors.New("invalid application ID")
	}
	entryID := strings.TrimSpace(requestedEntryID)
	if entryID == "" {
		entryID = SuggestEntryID(displayName)
	}
	if entryID == "" {
		entryID = "d" + internalID
	}
	if !entryPrefixPattern.MatchString(entryID) {
		return Identity{}, errors.New("must contain 1 to 27 lowercase letters, numbers, or internal hyphens and start with a letter")
	}
	appName, err := RenderAppNameTemplate(template, entryID)
	if err != nil {
		return Identity{}, err
	}
	return Identity{EntryID: entryID, AppName: appName}, nil
}

func ValidateEntryPrefixTemplate(template string) error {
	template = strings.TrimSpace(template)
	if template == "" {
		return errors.New("must not be empty")
	}
	if strings.Count(template, "{id}") != 1 {
		return errors.New("must contain the {id} placeholder exactly once")
	}
	literal := strings.Replace(template, "{id}", "", 1)
	if !strings.ContainsAny(literal, "abcdefghijklmnopqrstuvwxyz0123456789") {
		return errors.New("must include a fixed lowercase identifier in addition to {id}")
	}
	for _, entryID := range []string{"app", strings.Repeat("a", 27)} {
		if _, err := RenderAppNameTemplate(template, entryID); err != nil {
			return err
		}
	}
	return nil
}

// RenderAppNameTemplate expands the complete fnOS ID template. DockFN does
// not append a suffix: dots, namespaces and suffixes are all explicit policy.
func RenderAppNameTemplate(template, entryID string) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		template = DefaultEntryPrefixTemplate
	}
	if strings.Count(template, "{id}") != 1 {
		return "", errors.New("must contain the {id} placeholder exactly once")
	}
	if !entryPrefixPattern.MatchString(entryID) {
		return "", errors.New("invalid application entry ID")
	}
	for index := 0; index < len(template); {
		if strings.HasPrefix(template[index:], "{id}") {
			index += len("{id}")
			continue
		}
		ch := template[index]
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' && ch != '.' {
			return "", errors.New("may contain only lowercase letters, numbers, dots, hyphens, and {id}")
		}
		index++
	}
	appName := strings.Replace(template, "{id}", entryID, 1)
	if !IsOwnedAppName(appName) {
		return "", errors.New("renders to an invalid fnOS ID; it must start with a letter, use safe dot-separated lowercase labels, and contain at most 63 characters")
	}
	return appName, nil
}

func EffectiveEntryID(spec AppSpec) string {
	if entryPrefixPattern.MatchString(spec.EntryID) {
		return spec.EntryID
	}
	// 0.1.0 records did not persist EntryID and always used the .dkfn suffix.
	return EntryPrefix(spec.AppName)
}

// SuggestEntryID converts Chinese characters to tone-free pinyin, keeps
// lowercase ASCII letters and numbers, and collapses all other runs to one
// hyphen. Numeric-leading suggestions receive an app- prefix so the result is
// valid before it reaches the fnOS identity template.
func SuggestEntryID(displayName string) string {
	args := pinyin.NewArgs()
	args.Style = pinyin.Normal
	parts := make([]string, 0, len([]rune(displayName)))
	var ascii strings.Builder
	flushASCII := func() {
		if ascii.Len() > 0 {
			parts = append(parts, ascii.String())
			ascii.Reset()
		}
	}
	for _, char := range strings.TrimSpace(displayName) {
		if char <= unicode.MaxASCII && ((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')) {
			ascii.WriteRune(unicode.ToLower(char))
			continue
		}
		flushASCII()
		if values := pinyin.SinglePinyin(char, args); len(values) > 0 && values[0] != "" {
			parts = append(parts, values[0])
		}
	}
	flushASCII()
	slug := strings.Trim(strings.Join(parts, "-"), "-")
	if slug == "" {
		return ""
	}
	if slug[0] >= '0' && slug[0] <= '9' {
		slug = "app-" + slug
	}
	if len(slug) > 27 {
		truncated := strings.TrimRight(slug[:27], "-")
		if split := strings.LastIndexByte(truncated, '-'); split >= 3 {
			truncated = truncated[:split]
		}
		slug = truncated
	}
	if !entryPrefixPattern.MatchString(slug) {
		return ""
	}
	return slug
}
