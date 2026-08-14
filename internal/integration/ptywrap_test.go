//go:build !windows

package integration

import (
	"strings"
	"testing"
)

func TestStatusPaintSequenceReservesBottomRowAndTruncates(t *testing.T) {
	sequence := statusPaintSequence("TOKENHAWK codex in 11.80M cache 96.6%", 20, 40)
	if !strings.HasPrefix(sequence, "\x1b7\x1b[40;1H\x1b[0m\x1b[2K") || !strings.HasSuffix(sequence, "\x1b[0m\x1b8") {
		t.Fatalf("paint sequence does not save, address, clear, and restore: %q", sequence)
	}
	if !strings.Contains(sequence, "…") {
		t.Fatalf("overlong status line was not truncated: %q", sequence)
	}
	if strings.Contains(sequence, "96.6%") {
		t.Fatalf("truncation kept content beyond the terminal width: %q", sequence)
	}
}

func TestWrapWithoutTerminalFailsCleanlyInPTYMode(t *testing.T) {
	// Tests run without a controlling terminal on stdin, so the pty fallback
	// must refuse rather than corrupt the parent terminal state.
	err := Wrap("codex", nil, true)
	if err == nil {
		t.Fatal("pty wrap succeeded without a terminal")
	}
}
