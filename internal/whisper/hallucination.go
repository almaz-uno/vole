package whisper

import (
	"regexp"
	"strings"
	"unicode"
)

// Classic whisper.cpp hallucinations on silence, music, or very quiet speech.
// Matched as the whole transcript or as a trailing clause.
var hallucinationPhrases = []string{
	"продолжение следует",
	"спасибо за просмотр",
	"спасибо за внимание",
	"субтитры создавал dimatorzok",
	"субтитры сделал dimatorzok",
	"редактор субтитров",
	"подписывайтесь на канал",
	"ставьте лайки",
	"thanks for watching",
	"thank you for watching",
	"please subscribe",
	"like and subscribe",
	"subscribe",
}

var trailJunk *regexp.Regexp

func init() {
	parts := make([]string, len(hallucinationPhrases))
	for i, p := range hallucinationPhrases {
		parts[i] = regexp.QuoteMeta(p)
	}
	trailJunk = regexp.MustCompile(`(?i)\s+(` + strings.Join(parts, "|") + `)[\s\p{P}]*$`)
}

func canon(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ScrubHallucination removes known non-speech whisper fillers. It returns ""
// when the whole transcript is junk (caller should not inject or record it).
func ScrubHallucination(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	n := canon(s)
	for _, p := range hallucinationPhrases {
		if n == canon(p) {
			return ""
		}
	}
	if loc := trailJunk.FindStringIndex(s); loc != nil {
		s = strings.TrimSpace(s[:loc[0]])
		return ScrubHallucination(s)
	}
	return s
}
