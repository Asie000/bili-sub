package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"bilibili-sub/internal/bili"
)

// reShortLink matches a b23.tv share link. The path is an opaque token (e.g.
// b23.tv/aBcDeF), so the real bilibili URL only appears after a redirect -- this
// is why short links are resolved before ParseURL rather than inside it.
var reShortLink = regexp.MustCompile(`(?i)^(?:https?://)?b23\.tv/`)

// IsShortLink reports whether raw is a b23.tv share link.
func IsShortLink(raw string) bool {
	return reShortLink.MatchString(strings.TrimSpace(raw))
}

// ResolveShortLink follows a b23.tv link to the real bilibili URL. Anything that
// is not a short link is returned unchanged, so every input can be run through
// it unconditionally before parsing.
//
// A hop that never leaves b23.tv is an error, not a silent pass-through: parsing
// the short URL itself would only ever fail later with a confusing message.
func ResolveShortLink(ctx context.Context, c *bili.Client, raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if !IsShortLink(s) {
		return s, nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	final, err := c.ResolveURL(ctx, s)
	if err != nil {
		return "", fmt.Errorf("解析 b23.tv 短链失败：%w", err)
	}
	if IsShortLink(final) {
		return "", fmt.Errorf("b23.tv 短链没有跳转到 B站地址：%q", final)
	}
	return final, nil
}
