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

func TestParseAgyUsesConversationAndCumulativeTokens(t *testing.T) {
	session, err := ParseAgy(strings.NewReader(`{
		"session_id":"legacy-id",
		"conversation_id":"agy-123",
		"cwd":"/fallback",
		"model":{"id":"Gemini 3.5 Flash (High)"},
		"workspace":{"project_dir":"/work/tokenhawk"},
		"context_window":{
			"total_input_tokens":88244,
			"total_output_tokens":61074,
			"current_usage":{
				"input_tokens":63382,
				"output_tokens":346,
				"cache_creation_input_tokens":10,
				"cache_read_input_tokens":20857
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if session.Provider != core.Agy || session.ID != "agy-123" || session.Project != "/work/tokenhawk" || !session.Active {
		t.Fatalf("unexpected AGY session: %#v", session)
	}
	usage := session.Usage[0]
	if usage.Model != "gemini-3.5-flash" || usage.Input != 88244 || usage.CachedInput != 20857 || usage.CacheCreation != 10 || usage.Output != 61074 || usage.Total != 149318 {
		t.Fatalf("unexpected AGY usage: %#v", usage)
	}
}

func TestParseAgyFallsBackToCurrentUsage(t *testing.T) {
	session, err := ParseAgy(strings.NewReader(`{
		"session_id":"agy-legacy",
		"model":{"display_name":"Claude Sonnet 4.6 (Thinking)"},
		"workspace":{"current_dir":"/work/agy"},
		"context_window":{"current_usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":40}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	usage := session.Usage[0]
	if session.ID != "agy-legacy" || usage.Model != "claude-sonnet-4.6" || usage.Input != 140 || usage.CachedInput != 40 || usage.Output != 20 || usage.Total != 160 {
		t.Fatalf("unexpected AGY fallback: %#v", session)
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
