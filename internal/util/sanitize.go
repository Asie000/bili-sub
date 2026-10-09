package util

import (
	"strings"
	"unicode"
)

var badChars = strings.NewReplacer(
	`\`, "_", `/`, "_", `:`, "_", `*`, "_", `?`, "_",
	`"`, "_", `<`, "_", `>`, "_", `|`, "_",
	"\r", "", "\n", " ", "\t", " ",
)

// sanitizeText applies the shared cleaning of every name written to disk: the
// illegal characters are replaced, control characters are dropped, the result is
// trimmed and it is truncated to maxRunes runes. It returns an empty string when
// nothing is left, so each caller can pick its own placeholder.
func sanitizeText(name string, maxRunes int) string {
	s := badChars.Replace(name)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > maxRunes {
		s = string(r[:maxRunes])
	}
	return s
}

func SanitizeFilename(title, bvid string) string {
	s := sanitizeText(title, 100)
	if s == "" {
		s = "untitled"
	}
	return s + " [" + bvid + "]"
}

// SanitizeDirName cleans an author name for use as a single zip folder. It
// shares SanitizeFilename's illegal-character cleaning but appends no bvid
// suffix, falls back to 未知UP主 when nothing is left, and truncates to 80 runes
// so one author folder cannot dominate the entry path.
func SanitizeDirName(name string) string {
	s := sanitizeText(name, 80)
	if s == "" {
		s = "未知UP主"
	}
	return s
}
