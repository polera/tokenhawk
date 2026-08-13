package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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

func TestSpendModelShowsTokensAndEffectiveAPIRatePricing(t *testing.T) {
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
			t.Fatalf("spend API-rate detail missing %q:\n%s", want, view)
		}
	}
	// The synthetic haiku identifier is intentionally not in the exact-match
	// catalog, so no guessed rate should be displayed for it.
	if strings.Contains(view, "claude-haiku-4-5\n      API rate:") {
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
	for _, want := range []string{"SPEND · last 7 days", "TOTAL", "636.0k", "i:o 23.5:1", "$5.500000 API rate", "claude-haiku-4-5"} {
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

func TestSpendTrendIncludesMissingDaysAndReconciledCost(t *testing.T) {
	start := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	records := []spendRecord{
		{day: start, usage: core.Usage{Total: 100}, apiRateCost: 1.25},
		{day: start.AddDate(0, 0, 2), usage: core.Usage{Total: 300}, reportedCost: 2.50},
	}
	trend := buildSpendTrend(records, 10)
	if len(trend.points) != 3 || trend.bucketDays != 1 {
		t.Fatalf("daily trend shape = %#v", trend)
	}
	if trend.points[0].tokens != 100 || trend.points[0].cost != 1.25 {
		t.Fatalf("first trend point = %#v", trend.points[0])
	}
	if trend.points[1].tokens != 0 || trend.points[1].cost != 0 {
		t.Fatalf("missing day was not represented as zero: %#v", trend.points[1])
	}
	if trend.points[2].tokens != 300 || trend.points[2].cost != 2.50 {
		t.Fatalf("last trend point = %#v", trend.points[2])
	}
	bounded := buildSpendTrend(records, 10, start.AddDate(0, 0, -2), start.AddDate(0, 0, 4))
	if len(bounded.points) != 7 || bounded.points[2].tokens != 100 || bounded.points[4].tokens != 300 {
		t.Fatalf("selected window was not preserved in trend: %#v", bounded)
	}
}

func TestSpendExportUsesDailyResolutionAndReconciledCurrentView(t *testing.T) {
	m := spendModel(t)
	day := m.sessions[0].UpdatedAt.UTC().Truncate(24 * time.Hour)
	m.spendSince = day.AddDate(0, 0, -2)
	m.spendSpec = "3d"
	m.reportedCostDays = []time.Time{day}
	m.reportedCosts = []core.ReportedCost{{
		Provider: core.Claude, Day: day, Model: "claude-opus-4-8",
		AmountNanoUSD: 1_500_000_000, Source: "anthropic_admin_cost",
	}}
	m.provider = core.Claude
	m.rebuild()
	m.spendSince = day.AddDate(0, 0, -2)
	m.spendSpec = day.AddDate(0, 0, -2).Format("2006-01-02")
	report := m.spendExportReport(day.Add(12 * time.Hour))
	if report.View.Provider != core.Claude || report.View.WindowSpec != m.spendSpec || report.View.TimeseriesResolution != "1 day UTC" {
		t.Fatalf("spend export did not capture current view: %#v", report.View)
	}
	if len(report.Timeseries) != 3 {
		t.Fatalf("spend export timeseries has %d points, want three daily points: %#v", len(report.Timeseries), report.Timeseries)
	}
	if report.Timeseries[0].Usage.Total != 0 || report.Timeseries[1].Usage.Total != 0 {
		t.Fatalf("spend export omitted zero-valued UTC days: %#v", report.Timeseries)
	}
	last := report.Timeseries[2]
	if last.Usage.Total != 531_000 || last.Cost.ReportedUSD != 1.5 || last.Cost.APIRateUSD != 0 {
		t.Fatalf("spend export did not use reconciled visible records: %#v", last)
	}
	if report.Totals.Usage.Total != last.Usage.Total || report.Totals.Cost.TotalUSD != 1.5 {
		t.Fatalf("spend export totals differ from timeseries: totals=%#v point=%#v", report.Totals, last)
	}
}

func TestSpendExportKeysCreateSpendSnapshotCommands(t *testing.T) {
	m := spendModel(t)
	m.tab = spendTab
	m.rebuild()
	for _, keyName := range []string{"e", "x"} {
		updated, cmd := m.Update(key(keyName))
		m = updated.(Model)
		if cmd == nil {
			t.Fatalf("%s did not create a spend export command", keyName)
		}
		message := cmd().(exportMsg)
		if message.err != nil {
			t.Fatal(message.err)
		}
		if !strings.Contains(message.path, "tokenhawk-spend-") {
			t.Fatalf("%s used a non-spend export path: %s", keyName, message.path)
		}
		if err := os.Remove(message.path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSpendTrendBucketsLongRangesAndRendersBothSeries(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	records := []spendRecord{
		{day: start, usage: core.Usage{Total: 100}, apiRateCost: 1},
		{day: start.AddDate(0, 0, 99), usage: core.Usage{Total: 200}, apiRateCost: 2},
	}
	trend := buildSpendTrend(records, 10)
	if len(trend.points) != 10 || trend.bucketDays != 10 {
		t.Fatalf("long trend was not condensed to ten 10-day buckets: %#v", trend)
	}
	m := Model{width: 38}
	view := m.spendTrend(records)
	plain := ansi.Strip(view)
	for _, want := range []string{"TOKEN USAGE & COST OVER TIME (UTC)", "5-day totals", "TOKENS", "COST (USD)", "peak 200", "peak $2.00", "Jan 01", "Apr 06"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("spend trend missing %q:\n%s", want, view)
		}
	}
	for _, want := range []string{"200", "$2.00", "└", "·"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("spend trend line chart missing %q:\n%s", want, view)
		}
	}
}

func TestSpendLineChartFitsRequestedWidthAndDrawsPoints(t *testing.T) {
	days := []time.Time{
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
	}
	chart := spendLineChart("TOKENS", []float64{1, 100, 50}, days, 40, func(value float64) string {
		return human(int64(value))
	}, titleStyle)
	for _, line := range strings.Split(chart, "\n") {
		if got := lipgloss.Width(line); got > 40 {
			t.Fatalf("line chart width = %d, want <= 40:\n%s", got, chart)
		}
	}
	plain := ansi.Strip(chart)
	if !strings.Contains(plain, "●") {
		t.Fatalf("short line chart did not render points:\n%s", chart)
	}
	if !strings.Contains(plain, "Aug 01") || !strings.Contains(plain, "Aug 03") {
		t.Fatalf("line chart did not label its UTC date axis:\n%s", chart)
	}
}

func TestNiceChartMaxProducesReadableTicks(t *testing.T) {
	for _, test := range []struct {
		peak, want float64
	}{
		{200, 300},
		{5_170_000, 6_000_000},
		{6.29, 7.5},
		{0, 1},
	} {
		if got := niceChartMax(test.peak, 3); got != test.want {
			t.Fatalf("niceChartMax(%v, 3) = %v, want %v", test.peak, got, test.want)
		}
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
	updated, _ := m.Update(key("3"))
	m = updated.(Model)
	if m.tab != spendTab {
		t.Fatalf("3 did not open the spend view: tab=%d", m.tab)
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

// The two cache-write tiers bill differently, so an API-rate cost that includes
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
			t.Fatalf("spend API-rate detail missing %q:\n%s", want, view)
		}
	}
}

func TestSpendUsesAnthropicReportedCostWithoutDoubleCountingClaudeAPIRate(t *testing.T) {
	m := spendModel(t)
	day := m.sessions[0].UpdatedAt.UTC()
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	m.reportedCostDays = []time.Time{day}
	m.reportedCosts = []core.ReportedCost{
		{Provider: core.Claude, Day: day, Model: "claude-opus-4-8", AmountNanoUSD: 1_250_000_000, Source: "anthropic_admin_cost"},
		{Provider: core.Claude, Day: day, Model: "claude-haiku-4-5", AmountNanoUSD: 250_000_000, Source: "anthropic_admin_cost"},
	}
	m.tab = spendTab
	m.rebuild()
	view := m.spendContent()
	for _, want := range []string{
		"$1.500000 reported + $1.000000 API rate",
		"Anthropic Admin API organization billing covers 1 UTC day(s)",
		"reported: $1.250000  ·  Anthropic Admin Cost API",
		"BY DAY (UTC)",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("reported spend view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "50.0k input × $5/M") {
		t.Fatalf("replaced Claude API-rate cost still displayed its calculation:\n%s", view)
	}

	m.search = "work"
	m.rebuild()
	view = m.spendContent()
	if !strings.Contains(view, "$5.500000 API rate") ||
		!strings.Contains(view, "billing is excluded while search is active") {
		t.Fatalf("search did not fall back to attributable session API-rate costs:\n%s", view)
	}
}
