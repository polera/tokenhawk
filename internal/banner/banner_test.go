package banner

import (
	"strings"
	"testing"
)

func TestArtNonEmpty(t *testing.T) {
	if strings.TrimSpace(Art()) == "" {
		t.Fatal("Art() is empty; embed failed")
	}
	if !strings.Contains(Art(), "\x1b[") {
		t.Error("Art() has no ANSI color escapes")
	}
}

func TestPlainStripsEscapes(t *testing.T) {
	if strings.Contains(Plain(), "\x1b") {
		t.Fatal("Plain() still contains escape sequences")
	}
	if !strings.Contains(Plain(), "█") {
		t.Error("Plain() lost the block glyphs")
	}
}
