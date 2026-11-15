package notify

import (
	"strings"
	"unicode"
)

// clean strips control and bidi-override characters and bounds the length
// of text (it can come from other devices).
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case unicode.IsControl(r), r >= '‪' && r <= '‮', r >= '⁦' && r <= '⁩':
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}
