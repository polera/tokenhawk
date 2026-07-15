// Package banner renders the TokenHawk thunderbird splash art.
package banner

import (
	_ "embed"
	"strings"
)

//go:embed hawk.ansi
var hawk string

// Art returns the raw thunderbird banner, including its embedded 24-bit color
// escape sequences and a trailing reset. Terminals without truecolor support
// will approximate the colors; the shape still renders.
func Art() string {
	return hawk
}

// Plain returns the banner with all ANSI escape sequences stripped, for logs,
// non-TTY output, or NO_COLOR environments.
func Plain() string {
	var b strings.Builder
	b.Grow(len(hawk))
	inEsc := false
	for i := 0; i < len(hawk); i++ {
		c := hawk[i]
		switch {
		case inEsc:
			if c == 'm' {
				inEsc = false
			}
		case c == 0x1b:
			inEsc = true
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
