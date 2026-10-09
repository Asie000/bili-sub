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

// SanitizeSubtitleName builds the flat filename stem shared by the export zip
// and the on-disk download. It prefixes the video's UP主 so a flat archive still
// shows whose video each file is: the combined "author - title" is cleaned like
// SanitizeFilename and truncated to 100 runes, then the [bvid] tag is appended
// after the truncation so the video id always survives. An empty author falls
// back to the bare title, and an empty result to "untitled"; when bvid is empty
// the bracket suffix is omitted entirely.
func SanitizeSubtitleName(author, title, bvid string) string {
	combined := title
	if strings.TrimSpace(author) != "" {
		combined = author + " - " + title
	}
	s := sanitizeText(combined, 100)
	if s == "" {
		s = "untitled"
	}
	if bvid != "" {
		s += " [" + bvid + "]"
	}
	return s
}
