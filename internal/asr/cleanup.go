package asr

import (
	"regexp"
	"strings"
	"unicode"

	"bilibili-sub/internal/ir"
)

// CleanupOptions carries what segment cleanup needs to know about the run.
type CleanupOptions struct {
	// Language is the tag the caller asked for ("zh", "zh-CN", "en", or "" for
	// auto-detect). Only Chinese is eligible for the traditional-to-simplified
	// fold, so a Japanese transcript keeps its kanji.
	Language string
	// Simplify folds traditional Chinese characters to their simplified forms.
	Simplify bool
}

// whisperToken matches the special tokens whisper writes into the text
// (<|0.00|>, <|startoftranscript|>, <|zh|>, ...). They leak through whenever the
// timestamp decoding falls back to raw tokens, and they must never reach a
// subtitle file.
var whisperToken = regexp.MustCompile(`<\|[^|]*\|>`)

// CleanSegments returns the segments worth storing, in order.
//
// It drops what the model could not decode and what carries no text, removes the
// special tokens and the spaces whisper inserts inside Chinese sentences, folds
// traditional characters when asked to, and collapses a consecutive run of the
// same line -- the shape whisper's hallucination loop takes over silence or
// music. Timing of a kept segment is untouched, and a line returns unchanged
// when it has nothing wrong with it.
func CleanSegments(segments []ir.Segment, opts CleanupOptions) []ir.Segment {
	if len(segments) == 0 {
		return nil
	}
	simplify := opts.Simplify && isChineseLanguage(opts.Language)

	out := make([]ir.Segment, 0, len(segments))
	prevKey := ""
	for _, s := range segments {
		if isGarbled(s.Text) {
			continue
		}
		text := cleanText(s.Text)
		if simplify {
			text = toSimplified(text)
		}
		if text == "" {
			continue
		}

		key := dedupKey(text)
		// Only a consecutive run collapses, and only when the line is longer
		// than one character: whisper's loop repeats a whole sentence, while
		// "嗯" twice is simply what the speaker said.
		if len([]rune(key)) >= 2 && key == prevKey {
			continue
		}
		prevKey = key

		out = append(out, ir.Segment{
			StartMs: s.StartMs,
			EndMs:   s.EndMs,
			Text:    text,
			Speaker: s.Speaker,
		})
	}
	return out
}

// isChineseLanguage reports whether the tag names Chinese. An empty tag counts:
// the ASR leg only runs for Chinese or English, and the character table simply
// has nothing to match in non-Chinese text.
func isChineseLanguage(lang string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	return l == "" || l == "chinese" || strings.HasPrefix(l, "zh")
}

// isGarbled reports whether a line is too undecodable to keep. Whisper writes
// U+FFFD for audio it could not make sense of; one stray character is worth
// cleaning out of a line, a line made of them is not worth keeping. The ratio
// only applies to lines long enough for it to mean something.
func isGarbled(s string) bool {
	n := strings.Count(s, "\uFFFD")
	if n == 0 {
		return false
	}
	if n >= 3 || strings.Contains(s, "\uFFFD\uFFFD") {
		return true
	}
	total := len([]rune(strings.Join(strings.Fields(s), "")))
	return total >= 10 && float64(n)/float64(total) >= 0.2
}

// cleanText removes the artefacts whisper leaves inside one line: its special
// tokens, undecodable bytes, and the spaces it sometimes inserts between
// Chinese characters.
func cleanText(s string) string {
	s = whisperToken.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\uFFFD", "")
	s = strings.Join(strings.Fields(s), " ")
	return dropSpacesBetweenCJK(strings.TrimSpace(s))
}

// dropSpacesBetweenCJK removes a space with Chinese characters on both sides --
// how whisper renders a pause inside a Chinese sentence. A space touching latin
// text is real and stays, so "用 Go 写" is untouched.
func dropSpacesBetweenCJK(s string) string {
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	for i, r := range runes {
		if r == ' ' && len(out) > 0 && i+1 < len(runes) &&
			unicode.Is(unicode.Han, out[len(out)-1]) && unicode.Is(unicode.Han, runes[i+1]) {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// toSimplified folds every traditional character in s to its simplified
// equivalent. It is character-level: a character that is the same in both
// scripts is not in the table, and a context-dependent one takes its most common
// simplified form.
func toSimplified(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if simp, ok := tradToSimp[r]; ok {
			b.WriteRune(simp)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// dedupKey reduces a line to what matters when deciding whether it repeats: the
// characters themselves, without punctuation or spacing.
func dedupKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
