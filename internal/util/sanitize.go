package util

import "strings"

var badChars = strings.NewReplacer(
	`\`, "_", `/`, "_", `:`, "_", `*`, "_", `?`, "_",
	`"`, "_", `<`, "_", `>`, "_", `|`, "_",
	"\r", "", "\n", " ", "\t", " ",
)

func SanitizeFilename(title, bvid string) string {
	s := badChars.Replace(title)
	s = strings.TrimSpace(s)
	r := []rune(s)
	const maxTitle = 100
	if len(r) > maxTitle {
		r = r[:maxTitle]
		s = string(r)
	}
	if s == "" {
		s = "untitled"
	}
	return s + " [" + bvid + "]"
}
