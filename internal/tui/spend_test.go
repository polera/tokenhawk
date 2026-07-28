package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/polera/tokenhawk/internal/core"
	"github.com/polera/tokenhawk/internal/pricing"
)

func spendModel(t *testing.T) Model {
	t.Helper()
	now := time.Now()
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, prices)
	m.sessions = []core.Session{
		{Provider: core.Claude, ID: "today-a", Project: "/work/a", Active: true, UpdatedAt: now.Add(-2 * time.Hour),
			Usage:     []core.Usage{{Model: "claude-opus-4-8", Input: 500_000, CachedInput: 450_000, Output: 20_000, Total: 520_000, CostUSD: 4, PricingStatus: "priced"}},
			Subagents: []core.Subagent{{ID: "child", ParentID: "today-a", Usage: []core.Usage{{Model: "claude-haiku-4-5", Input: 10_000, CachedInput: 5_000, Output: 1_000, Total: 11_000, CostUSD: 0.5, PricingStatus: "priced"}}}}},
		{Provider: core.Codex, ID: "recent-b", Project: "/work/b", UpdatedAt: now.Add(-3 * 24 * time.Hour),
			Usage: []core.Usage{{Model: "gpt-5", Input: 100_000, CachedInput: 90_000, Output: 5_000, Total: 105_000, CostUSD: 1, PricingStatus: "priced"}}},
		{Provider: core.Gemini, ID: "old-c", Project: "/work/c", UpdatedAt: now.Add(-40 * 24 * time.Hour),
			Usage: []core.Usage{{Model: "gemini-3-pro", Input: 900_000, CachedInput: 10_000, Output: 90_000, Total: 990_000, CostUSD: 9, PricingStatus: "priced"}}},
	}
	m.width, m.height = 140, 40
	m.resize()
	return m
}

func TestSpendModelShowsTokensAndEffectivePricingBehindEstimate(t *testing.T) {
	m := spendModel(t)
	m.tab = spendTab
	m.rebuild()
	view := m.spendContent()
	for _, want := range []string{
		"50.0k input × $5/M",
		"450.0k cached input × $0.5/M",
		"0 cache write × $6.25/M",
		"20.0k output × $25/M",
		"effective 2026-05-28",
		"10.0k input × $1.25/M",
		"90.0k cached input × $0.125/M",
		"5.0k output × $10/M",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("spend estimate missing %q:\n%s", want, view)
		}
	}
	// The synthetic haiku identifier is intentionally not in the exact-match
	// catalog, so no guessed rate should be displayed for it.
	if strings.Contains(view, "claude-haiku-4-5\n      estimate:") {
		t.Fatalf("unpriced model received a guessed breakdown:\n%s", view)
	}
}

func TestSpendPricingBreakdownWrapsForNarrowTerminals(t *testing.T) {
	m := spendModel(t)
	m.width = 80
	m.tab = spendTab
	m.rebuild()
	for _, group := range groupByModel(m.shown) {
		if group.name != "claude-opus-4-8" {
			continue
		}
		details := m.modelPricingDetails(group.records)
		if len(details) != 3 || !strings.Contains(details[1], "20.0k output × $25/M") ||
			!strings.Contains(details[2], "=  $0.975000  ·  claude rate effective 2026-05-28") {
			t.Fatalf("narrow pricing breakdown was not split cleanly: %#v", details)
		}
		return
	}
	t.Fatal("opus model group not found")
}

func TestSpendModelSeparatesEffectiveRatesAndBillsGeminiReasoningAsOutput(t *testing.T) {
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, prices)
	m.sessions = []core.Session{
		{Provider: core.Claude, UpdatedAt: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), Usage: []core.Usage{{Model: "claude-sonnet-5", Input: 100, Output: 10, PricingStatus: "priced"}}},
		{Provider: core.Claude, UpdatedAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), Usage: []core.Usage{{Model: "claude-sonnet-5", Input: 200, Output: 20, PricingStatus: "priced"}}},
		{Provider: core.Gemini, UpdatedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Usage: []core.Usage{{Model: "gemini-2.5-pro", Output: 30, Reasoning: 20, PricingStatus: "priced"}}},
	}
	m.shown = m.sessions
	m.width = 140
	view := m.spendContent()
	for _, want := range []string{
		"100 input × $2/M",
		"10 output × $10/M",
		"effective 2026-06-30",
		"200 input × $3/M",
		"20 output × $15/M",
		"effective 2026-09-01",
		"50 output + reasoning × $10/M",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("effective-rate breakdown missing %q:\n%s", want, view)
		}
	}
}

func TestSpendWindowExcludesSessionsUpdatedBeforeSince(t *testing.T) {
	m := spendModel(t)
	m.tab = spendTab
	m.rebuild()
	if len(m.shown) != 2 {
		t.Fatalf("default 7d window kept %d sessions, want the two recent ones", len(m.shown))
	}
	view := m.spendContent()
	if strings.Contains(view, "gemini") {
		t.Fatalf("40-day-old session leaked into the 7d window:\n%s", view)
	}
	// Totals must include subagent usage, matching Session.Totals.
	for _, want := range []string{"SPEND · last 7 days", "TOTAL", "636.0k", "i:o 23.5:1", "$5.500000 estimated (priced)", "claude-haiku-4-5"} {
		if !strings.Contains(view, want) {
			t.Fatalf("spend view missing %q:\n%s", want, view)
		}
	}
	if err := m.setSpendSpec("all"); err != nil {
		t.Fatal(err)
	}
	m.rebuild()
	if len(m.shown) != 3 || !strings.Contains(m.spendContent(), "all time") {
		t.Fatalf("all-time window did not include every session: %d shown", len(m.shown))
	}
}

func TestSpendGroupsShowInputOutputRatiosAtEveryWidth(t *testing.T) {
	for _, width := range []int{80, 140} {
		m := spendModel(t)
		m.tab = spendTab
		m.width = width
		m.resize()
		view := m.spendContent()
		for _, want := range []string{"i:o 24.3:1", "i:o 20.0:1"} {
			if !strings.Contains(view, want) {
				t.Fatalf("width %d spend view missing %q:\n%s", width, want, view)
			}
		}
	}
}

func TestSpendGroupsRankProvidersModelsAndDays(t *testing.T) {
	m := spendModel(t)
	m.tab = spendTab
	if err := m.setSpendSpec("all"); err != nil {
		t.Fatal(err)
	}
	m.rebuild()
	providers := groupByProvider(m.shown)
	if len(providers) != 3 || providers[0].name != "gemini" || providers[1].name != "claude" {
		t.Fatalf("providers were not ranked by cost: %#v", providers)
	}
	models := groupByModel(m.shown)
	if models[0].name != "gemini-3-pro" || len(models) != 4 {
		t.Fatalf("models were not broken out per model: %#v", models)
	}
	days := groupByDay(m.shown)
	if len(days) != 3 || !(days[0].name > days[1].name && days[1].name > days[2].name) {
		t.Fatalf("days were not ordered newest first: %#v", days)
	}
	if total := core.SumUsage(spendUsage(m.shown)); total.CostUSD != 14.5 || total.Total != 1_626_000 {
		t.Fatalf("window totals do not sum every session and subagent: %#v", total)
	}
}

func TestSpendKeysCycleWindowsAndAcceptTypedDates(t *testing.T) {
	m := spendModel(t)
	updated, _ := m.Update(key("4"))
	m = updated.(Model)
	if m.tab != spendTab {
		t.Fatalf("4 did not open the spend view: tab=%d", m.tab)
	}
	updated, _ = m.Update(key("t"))
	m = updated.(Model)
	if m.spendSpec != "30d" {
		t.Fatalf("t did not advance the window from 7d: %q", m.spendSpec)
	}
	updated, _ = m.Update(key("d"))
	m = updated.(Model)
	if !m.sinceInput {
		t.Fatal("d did not open the since prompt")
	}
	for _, k := range []string{"ctrl+a", "backspace", "backspace", "backspace", "2", "0", "2", "6", "-", "0", "7", "-", "0", "1", "enter"} {
		updated, _ = m.Update(key(k))
		m = updated.(Model)
	}
	if m.sinceInput {
		t.Fatalf("enter did not apply the typed window: draft=%q", m.sinceDraft)
	}
	if m.spendSpec != "2026-07-01" {
		t.Fatalf("typed window was not applied: %q", m.spendSpec)
	}
	// An unusable window keeps the prompt open with the last good bound intact.
	before := m.spendSince
	updated, _ = m.Update(key("d"))
	m = updated.(Model)
	for _, k := range []string{"backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "z", "enter"} {
		updated, _ = m.Update(key(k))
		m = updated.(Model)
	}
	if !m.sinceInput || !m.spendSince.Equal(before) {
		t.Fatalf("bad window was accepted: sinceInput=%v since=%s", m.sinceInput, m.spendSince)
	}
	if !strings.Contains(m.notice, "since:") {
		t.Fatalf("bad window reported no reason: %q", m.notice)
	}
	updated, _ = m.Update(key("esc"))
	m = updated.(Model)
	updated, _ = m.Update(key("1"))
	m = updated.(Model)
	if m.sinceInput || m.tab != 0 {
		t.Fatalf("spend view did not return to the session list: sinceInput=%v tab=%d", m.sinceInput, m.tab)
	}
}

func TestSpendBodyFitsTheViewportAndScrolls(t *testing.T) {
	m := spendModel(t)
	m.tab = spendTab
	if err := m.setSpendSpec("all"); err != nil {
		t.Fatal(err)
	}
	m.height = 20
	m.resize()
	body := m.spendBody()
	if got, limit := len(strings.Split(body, "\n")), m.spendViewport(); got > limit {
		t.Fatalf("spend body rendered %d lines into a %d-line viewport", got, limit)
	}
	if !strings.Contains(body, "SPEND") {
		t.Fatalf("clipped body lost its heading:\n%s", body)
	}
	if m.spendMaxOffset() == 0 {
		t.Fatal("a clipped report reported nothing to scroll")
	}
	m.scrollSpend(1000)
	if m.spendOffset != m.spendMaxOffset() {
		t.Fatalf("scroll ran past the end: %d of %d", m.spendOffset, m.spendMaxOffset())
	}
	if lines := strings.Split(m.spendBody(), "\n"); len(lines) > m.spendViewport() {
		t.Fatalf("scrolled body overflowed the viewport: %d lines", len(lines))
	}
	m.scrollSpend(-1000)
	if m.spendOffset != 0 {
		t.Fatalf("scroll ran past the start: %d", m.spendOffset)
	}
}

func TestSpendViewReportsAnEmptyWindow(t *testing.T) {
	m := spendModel(t)
	m.tab = spendTab
	if err := m.setSpendSpec("1h"); err != nil {
		t.Fatal(err)
	}
	m.rebuild()
	if got := m.spendContent(); !strings.Contains(got, "No sessions were updated in this window") {
		t.Fatalf("empty window rendered a misleading report:\n%s", got)
	}
}

func key(s string) tea.KeyPressMsg {
	if len(s) == 1 {
		return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
	codes := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "backspace": tea.KeyBackspace}
	if code, ok := codes[s]; ok {
		return tea.KeyPressMsg{Code: code}
	}
	return tea.KeyPressMsg{Code: rune(s[len(s)-1]), Mod: tea.ModCtrl}
}

// The two cache-write tiers bill differently, so an estimate that includes
// hourly writes has to show both rates rather than one blended line.
func TestSpendBreakdownSeparatesCacheWriteTiers(t *testing.T) {
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, prices)
	m.width, m.height = 140, 40
	m.resize()
	m.sessions = []core.Session{{
		Provider: core.Claude, ID: "a", UpdatedAt: time.Now(),
		Usage: []core.Usage{{Model: "claude-opus-4-8", CacheCreation: 1_000_000, CacheCreation1h: 600_000,
			Total: 1_000_000, PricingStatus: "priced"}},
	}}
	m.tab = spendTab
	m.rebuild()
	view := m.spendContent()
	for _, want := range []string{
		"400.0k cache write 5m × $6.25/M",
		"600.0k cache write 1h × $10/M",
		"$8.500000",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("spend estimate missing %q:\n%s", want, view)
		}
	}
}
