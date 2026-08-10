package mail

import (
	"regexp"
	"strings"
)

var prefixPattern = regexp.MustCompile(`(?i)^\s*((re|fw|fwd|aw|sv|wg|答复|回复)\s*:\s*)+`)

// NormalizeSubject removes repeated reply/forward prefixes and normalizes whitespace.
func NormalizeSubject(subject string) string {
	clean := prefixPattern.ReplaceAllString(strings.TrimSpace(subject), "")
	return strings.ToLower(strings.Join(strings.Fields(clean), " "))
}

// ReplySubject returns a display subject with exactly one Re: prefix.
func ReplySubject(subject string) string {
	clean := prefixPattern.ReplaceAllString(strings.TrimSpace(subject), "")
	if clean == "" {
		return "Re: (No subject)"
	}
	return "Re: " + strings.Join(strings.Fields(clean), " ")
}

// References appends a message identifier once while preserving order.
func References(existing, messageID string) string {
	seen := make(map[string]struct{})
	var result []string
	for _, identifier := range append(strings.Fields(existing), messageID) {
		identifier = strings.TrimSpace(identifier)
		if identifier == "" {
			continue
		}
		if _, ok := seen[identifier]; ok {
			continue
		}
		seen[identifier] = struct{}{}
		result = append(result, identifier)
	}
	return strings.Join(result, " ")
}
