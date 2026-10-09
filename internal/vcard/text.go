package vcard

import "strings"

// TrimPrefixFold removes a case-insensitive URI scheme prefix.
func TrimPrefixFold(value, prefix string) string {
	if len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix) {
		return value[len(prefix):]
	}
	return value
}
