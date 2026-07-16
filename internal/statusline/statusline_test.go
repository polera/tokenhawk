package statusline

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/polera/tokenhawk/internal/core"
)

func TestParseClaudeUsesStableSessionAndWorkspace(t *testing.T) {
	selector, err := ParseClaude(strings.NewReader(`{"session_id":"claude-123","workspace":{"current_dir":"/work/tokenhawk"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if selector.Provider != core.Claude || selector.SessionID != "claude-123" || selector.Project != "/work/tokenhawk" {
		t.Fatalf("unexpected selector: %#v", selector)
	}
}

func TestSelectReturnsOneExactSession(t *testing.T) {
	sessions := []core.Session{
		{Provider: core.Claude, ID: "wanted", Project: "/work/tokenhawk", UpdatedAt: time.Unix(1, 0), Usage: []core.Usage{{Input: 10}}},
		{Provider: core.Claude, ID: "other", Project: "/work/tokenhawk", UpdatedAt: time.Unix(2, 0), Usage: []core.Usage{{Input: 999}}},
	}
	selected, ok := Select(sessions, Selector{Provider: core.Claude, SessionID: "wanted", Project: "/work/tokenhawk"})
	if !ok || selected.ID != "wanted" || selected.Totals().Input != 10 {
		t.Fatalf("selected cumulative or wrong session: %#v, %v", selected, ok)
	}
}

func TestSelectMissingExactSessionTerminates(t *testing.T) {
	if selected, ok := Select(nil, Selector{Provider: core.Claude, SessionID: "missing", Project: "/work/tokenhawk"}); ok {
		t.Fatalf("selected missing session: %#v", selected)
	}
	if selected, ok := Select(nil, Selector{Provider: core.Claude, SessionID: "missing"}); ok {
		t.Fatalf("selected missing session without project: %#v", selected)
	}
}

func TestSelectProjectPrefersActiveThenNewest(t *testing.T) {
	sessions := []core.Session{
		{Provider: core.Codex, ID: "inactive-new", Project: "/work/tokenhawk", Active: false, UpdatedAt: time.Unix(30, 0)},
		{Provider: core.Codex, ID: "active-old", Project: "/work/tokenhawk", Active: true, UpdatedAt: time.Unix(10, 0)},
		{Provider: core.Codex, ID: "active-new", Project: "/work/tokenhawk", Active: true, UpdatedAt: time.Unix(20, 0)},
	}
	selected, ok := Select(sessions, Selector{Provider: core.Codex, Project: "/work/tokenhawk"})
	if !ok || selected.ID != "active-new" {
		t.Fatalf("unexpected project selection: %#v, %v", selected, ok)
	}
}

func TestSelectActiveOnlyDoesNotShowHistory(t *testing.T) {
	sessions := []core.Session{{Provider: core.Pi, ID: "old", Project: "/work/tokenhawk", Active: false}}
	if selected, ok := Select(sessions, Selector{Provider: core.Pi, Project: "/work/tokenhawk", Status: "active"}); ok {
		t.Fatalf("selected inactive history for live status: %#v", selected)
	}
}

func TestRenderPlainIncludesSessionMetrics(t *testing.T) {
	session := core.Session{
		Provider: core.Codex,
		ID:       "session",
		Usage: []core.Usage{{
			Input: 120_000, CachedInput: 108_000, Output: 10_000, Total: 130_000,
			CostUSD: 1.25, PricingStatus: "priced",
		}},
		Subagents: []core.Subagent{{ID: "a", Running: true}, {ID: "b", Running: false}},
	}
	line, err := Render(session, "plain")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"codex", "in 120.0k", "cache 90.0%", "out 10.0k", "I:O 12.0:1", "$1.2500", "1/2 agents"} {
		if !strings.Contains(line, want) {
			t.Fatalf("status omitted %q: %s", want, line)
		}
	}
	if strings.Contains(line, "LOW CACHE") {
		t.Fatalf("healthy cache ratio raised alarm: %s", line)
	}
}

func TestRenderUsesStandardBlueBrandColor(t *testing.T) {
	session := core.Session{Provider: core.Claude}

	ansi, err := Render(session, "ansi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ansi, "\x1b[38;2;5;169;199m") {
		t.Fatalf("ANSI status does not use standard blue: %q", ansi)
	}

	tmux, err := Render(session, "tmux")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tmux, "#[fg=#05A9C7,bold]") {
		t.Fatalf("tmux status does not use standard blue: %q", tmux)
	}
}

func TestRenderAlarmAndJSON(t *testing.T) {
	session := core.Session{Provider: core.Gemini, ID: "g", Active: true, Usage: []core.Usage{{Input: 200_000, CachedInput: 100_000, Output: 1_000, PricingStatus: "unpriced"}}}
	line, err := Render(session, "ansi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "LOW CACHE") || !strings.Contains(line, "\x1b[") {
		t.Fatalf("alarm styling missing: %q", line)
	}
	encoded, err := Render(session, "json")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal([]byte(encoded), &value); err != nil {
		t.Fatal(err)
	}
	if value["session_id"] != "g" || value["cache_alarm"] != true || value["cache_ratio"] != 0.5 {
		t.Fatalf("unexpected JSON status: %s", encoded)
	}
}

func TestWaitingSupportsTmux(t *testing.T) {
	line, err := Waiting(Selector{Provider: core.Codex}, "tmux")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "waiting for codex") || !strings.HasPrefix(line, "#[fg=#05A9C7,bold]") {
		t.Fatalf("unexpected waiting line: %s", line)
	}
}
