package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/polera/tokenhawk/internal/core"
)

func TestSessionRowsShowIndependentBreakdowns(t *testing.T) {
	m := New(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	m = updated.(Model)
	sessions := []core.Session{
		{Provider: core.Claude, ID: "session-a", Project: "/work/a", Active: true, UpdatedAt: time.Unix(2, 0), Usage: []core.Usage{{Model: "model-a", Input: 100, CachedInput: 40, Output: 20, Total: 120}}, Subagents: []core.Subagent{{ID: "agent-a", Running: true}}},
		{Provider: core.Codex, ID: "session-b", Project: "/work/b", Active: true, UpdatedAt: time.Unix(1, 0), Usage: []core.Usage{{Model: "model-b", Input: 7, CachedInput: 2, Output: 3, Total: 10}}},
	}
	updated, _ = m.Update(sessionsMsg{sessions: sessions})
	m = updated.(Model)
	rows := m.table.Rows()
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want one per session", len(rows))
	}
	if rows[0][2] != "1/1" || rows[0][3] != "100" || rows[0][4] != "40" || rows[0][5] != "20" || rows[0][6] != "5.00:1" || rows[0][7] != "120" {
		t.Fatalf("first session was not broken down independently: %#v", rows[0])
	}
	if rows[1][2] != "—" || rows[1][3] != "7" || rows[1][4] != "2" || rows[1][5] != "3" || rows[1][6] != "2.33:1" || rows[1][7] != "10" {
		t.Fatalf("second session was not broken down independently: %#v", rows[1])
	}
	if rows[0][8] != "unpriced" || rows[1][8] != "unpriced" {
		t.Fatalf("medium layout omitted pricing: %#v", rows)
	}
}

func TestHawkBrandHasCompactAndFullArtwork(t *testing.T) {
	if got := hawkBrand(40); !strings.Contains(got, "▒▓▓▓▓▒") || !strings.Contains(got, "TOKENHAWK") {
		t.Fatalf("compact brand missing hawk: %q", got)
	}
	if got := hawkBrand(120); !strings.Contains(got, "⢠⣤⣤⣤⣤") || !strings.Contains(got, "⠘⠛⠛⠛⠛") || !strings.Contains(got, "session token monitor") {
		t.Fatalf("full brand missing hawk: %q", got)
	}
}

func TestResizeAcrossColumnLayoutsRebuildsRowShape(t *testing.T) {
	m := New(nil)
	m.sessions = []core.Session{{Provider: core.Codex, ID: "session", Project: "/work", Active: true, UpdatedAt: time.Now(), Usage: []core.Usage{{Model: "model", Input: 10, CachedInput: 3, Output: 2, Reasoning: 1, Total: 12}}}}
	for _, tc := range []struct {
		width int
		cells int
	}{{110, 10}, {140, 12}, {50, 5}, {120, 12}, {79, 5}, {80, 10}} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: tc.width, Height: 30})
		m = updated.(Model)
		rows := m.table.Rows()
		if len(rows) != 1 || len(rows[0]) != tc.cells {
			t.Fatalf("width %d produced row shape %#v, want %d cells", tc.width, rows, tc.cells)
		}
		if m.table.Cursor() != 0 {
			t.Fatalf("width %d lost the selected session", tc.width)
		}
	}
}

func TestSessionDetailBreaksOutSubagents(t *testing.T) {
	now := time.Now()
	m := New(nil)
	m.sessions = []core.Session{{Provider: core.Codex, ID: "parent", Project: "/work", Active: true, UpdatedAt: now, Usage: []core.Usage{{Model: "parent-model", Total: 10}}, Subagents: []core.Subagent{
		{ID: "child-one", ParentID: "parent", Name: "Plato", AgentPath: "/root/review", UpdatedAt: now, Running: true, Status: "running", Usage: []core.Usage{{Model: "agent-model", Input: 30, CachedInput: 10, Output: 5, Total: 35, CostUSD: .01, PricingStatus: "priced"}}},
		{ID: "child-two", ParentID: "parent", UpdatedAt: now.Add(-time.Hour), Status: "inactive"},
	}}}
	m.width, m.height = 120, 40
	m.resize()
	m.detail = true
	view := m.detailView()
	for _, want := range []string{"SESSION TOTAL", "PARENT USAGE", "SUBAGENTS", "1 running / 2 total", "Plato", "child-one", "/root/review", "agent-model", "input:output 6.00:1", "$0.010000 estimated (priced)", "child-two"} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail missing %q:\n%s", want, view)
		}
	}
}

func TestInputOutputRatios(t *testing.T) {
	for _, tc := range []struct {
		input, output int64
		want          string
	}{{100, 20, "5.00:1"}, {20, 100, "1:5.00"}, {0, 10, "0:1"}, {10, 0, "∞:1"}, {0, 0, "—"}} {
		if got := ratioText(tc.input, tc.output); got != tc.want {
			t.Fatalf("ratioText(%d, %d) = %q, want %q", tc.input, tc.output, got, tc.want)
		}
	}
}

func TestHighInputLowCacheIsAlarmed(t *testing.T) {
	for _, tc := range []struct {
		u    core.Usage
		want bool
	}{
		{core.Usage{Input: 100_000, CachedInput: 79_999}, true},
		{core.Usage{Input: 100_000, CachedInput: 80_000}, false},
		{core.Usage{Input: 99_999, CachedInput: 0}, false},
	} {
		if got := cacheAlarm(tc.u); got != tc.want {
			t.Fatalf("cacheAlarm(%#v) = %v, want %v", tc.u, got, tc.want)
		}
	}

	m := New(nil)
	m.sessions = []core.Session{{Provider: core.Codex, ID: "alarm", Project: "/work", Active: true, UpdatedAt: time.Now(), Usage: []core.Usage{{Model: "model", Input: 200_000, CachedInput: 100_000, Output: 1_000, Total: 201_000}}}}
	m.width, m.height = 110, 30
	m.resize()
	row := strings.Join(m.table.Rows()[0], " ")
	if !strings.Contains(row, "⚠") {
		t.Fatalf("alarming row has no warning marker: %q", row)
	}
	m.detail = true
	if detail := m.detailView(); !strings.Contains(detail, "LOW CACHE") || !strings.Contains(detail, "50.0% cached") {
		t.Fatalf("detail omitted cache alarm: %s", detail)
	}
	usage := []core.Usage{{Input: 200_000, CachedInput: 100_000}}
	if got := activeCacheAlarms([]core.Session{{Active: true, Usage: usage}, {Active: false, Usage: usage}}); got != 1 {
		t.Fatalf("header cache alarms counted inactive sessions: %d", got)
	}
}

func TestActiveInactiveCommandTogglesLists(t *testing.T) {
	m := New(nil)
	m.sessions = []core.Session{
		{Provider: core.Codex, ID: "active", Active: true, UpdatedAt: time.Unix(3, 0)},
		{Provider: core.Claude, ID: "older", Active: false, UpdatedAt: time.Unix(1, 0)},
		{Provider: core.Gemini, ID: "newer", Active: false, UpdatedAt: time.Unix(2, 0)},
	}
	m.width, m.height = 110, 30
	m.resize()
	m.toggleActiveInactive()
	if m.detail || m.tab != 1 || len(m.shown) != 2 || m.shown[0].ID != "newer" {
		t.Fatalf("toggle did not show inactive sessions: detail=%v tab=%d shown=%#v", m.detail, m.tab, m.shown)
	}
	m.toggleActiveInactive()
	if m.tab != 0 || len(m.shown) != 1 || m.shown[0].ID != "active" {
		t.Fatalf("toggle did not return to active sessions: tab=%d shown=%#v", m.tab, m.shown)
	}
}

func TestResumeCommandsUseProviderSyntaxAndProjectDirectory(t *testing.T) {
	for _, tc := range []struct {
		provider core.Provider
		want     string
	}{
		{core.Claude, "cd '/work/team' && claude --resume 'session-id'"},
		{core.Codex, "cd '/work/team' && codex resume 'session-id'"},
		{core.Gemini, "cd '/work/team' && gemini --resume 'session-id'"},
		{core.Pi, "cd '/work/team' && pi --session 'session-id'"},
		{core.OpenCode, "cd '/work/team' && opencode --session 'session-id'"},
	} {
		got := resumeCommand(core.Session{Provider: tc.provider, ID: "session-id", Project: "/work/team"})
		if got != tc.want {
			t.Fatalf("resumeCommand(%s) = %q, want %q", tc.provider, got, tc.want)
		}
	}
	if got := shellQuote("/work/it's"); got != `'/work/it'"'"'s'` {
		t.Fatalf("unsafe shell quoting: %q", got)
	}
}

func TestReportedCostLabelIsNotEstimated(t *testing.T) {
	got := costDetail(core.Usage{CostUSD: 1.25, PricingStatus: "reported"})
	if got != "$1.250000 reported" {
		t.Fatalf("reported cost was relabeled: %q", got)
	}
}
