package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/polera/tokenhawk/internal/core"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// A suppressed estimate and a reported amount for the same day and model must
// land on one row, so the residual reflects both sides rather than double
// counting them as separate rows.
func TestReconcileRowsPairsEstimateAndReported(t *testing.T) {
	rows := reconcileRows([]spendRecord{
		{provider: core.Claude, day: day("2026-07-28"), usage: core.Usage{Model: "claude-opus-5"},
			estimateSuppressed: true, suppressedEstimate: 12.50},
		{provider: core.Claude, day: day("2026-07-28"), usage: core.Usage{Model: "claude-opus-5"},
			reportedCost: 12.00},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 paired row, got %d: %+v", len(rows), rows)
	}
	if rows[0].estimated != 12.50 || rows[0].reported != 12.00 {
		t.Fatalf("want est 12.50 / billed 12.00, got %+v", rows[0])
	}
	if got := rows[0].residual(); got < 0.49 || got > 0.51 {
		t.Fatalf("want residual ~0.50, got %v", got)
	}
}

// Estimates that were never suppressed belong to days Anthropic has not
// reported. Comparing them against a zero reported side would read as 100%
// drift, so they must not produce rows at all.
func TestReconcileRowsSkipsUncoveredEstimates(t *testing.T) {
	rows := reconcileRows([]spendRecord{
		{provider: core.Claude, day: day("2026-07-29"), usage: core.Usage{Model: "claude-opus-5"},
			estimatedCost: 9.99},
	})
	if len(rows) != 0 {
		t.Fatalf("want no rows for an uncovered day, got %+v", rows)
	}
}

func TestReconcileRowsIgnoresNonClaude(t *testing.T) {
	rows := reconcileRows([]spendRecord{
		{provider: core.Gemini, day: day("2026-07-28"), usage: core.Usage{Model: "gemini-3-pro"},
			estimateSuppressed: true, suppressedEstimate: 4},
	})
	if len(rows) != 0 {
		t.Fatalf("reconciliation is Claude-only, got %+v", rows)
	}
}

// Rows sort newest day first, then by residual magnitude, so the largest
// discrepancy on the most recent day is the first thing read.
func TestReconcileRowsSortsByDayThenResidual(t *testing.T) {
	rows := reconcileRows([]spendRecord{
		{provider: core.Claude, day: day("2026-07-27"), usage: core.Usage{Model: "old"},
			estimateSuppressed: true, suppressedEstimate: 50},
		{provider: core.Claude, day: day("2026-07-28"), usage: core.Usage{Model: "small"},
			estimateSuppressed: true, suppressedEstimate: 1},
		{provider: core.Claude, day: day("2026-07-28"), usage: core.Usage{Model: "big"},
			estimateSuppressed: true, suppressedEstimate: 30},
	})
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	if !rows[0].day.Equal(day("2026-07-28")) || rows[0].model != "big" {
		t.Fatalf("want newest day / largest residual first, got %+v", rows[0])
	}
	if rows[1].model != "small" {
		t.Fatalf("want smaller residual second within the same day, got %+v", rows[1])
	}
	if !rows[2].day.Equal(day("2026-07-27")) {
		t.Fatalf("want older day last, got %+v", rows[2])
	}
}

// A near-zero reported side makes a percentage meaningless, so the view must
// suppress the ratio rather than print a huge number from rounding noise.
func TestReconcileRowRatioGate(t *testing.T) {
	tiny := reconcileRow{estimated: 0.004, reported: 0.001}
	if tiny.hasRatio() {
		t.Fatal("sub-cent reported spend must not report a drift ratio")
	}
	real := reconcileRow{estimated: 11, reported: 10}
	if !real.hasRatio() {
		t.Fatal("cent-scale reported spend should report a ratio")
	}
	if got := signedPercent(real.driftRatio()); got != "+10.0%" {
		t.Fatalf("want +10.0%%, got %s", got)
	}
}

func TestSignedDollars(t *testing.T) {
	if got := signedDollars(1.5); got != "+$1.50" {
		t.Fatalf("want +$1.50, got %s", got)
	}
	if got := signedDollars(-1.5); got != "-$1.50" {
		t.Fatalf("want -$1.50, got %s", got)
	}
}

// An unpriced model returns a zero cost, which is indistinguishable from free
// unless it is named explicitly.
func TestUnpricedModels(t *testing.T) {
	got := unpricedModels([]spendRecord{
		{usage: core.Usage{Model: "claude-opus-9", Total: 100, PricingStatus: "unpriced"}},
		{usage: core.Usage{Model: "claude-opus-9", Total: 200, PricingStatus: "unpriced"}},
		{usage: core.Usage{Model: "claude-opus-5", Total: 100, PricingStatus: "priced"}},
		{usage: core.Usage{Model: "", Total: 100, PricingStatus: "unpriced"}},
	})
	if len(got) != 1 || got[0] != "claude-opus-9" {
		t.Fatalf("want [claude-opus-9], got %v", got)
	}
}

// With a covered day, the local estimate is suppressed in favor of billing and
// the reconcile view must show both sides plus the residual between them.
func TestReconcileContentReportsBothSides(t *testing.T) {
	m := spendModel(t)
	m.tab = reconcileTab
	today := time.Now().UTC().Truncate(24 * time.Hour)
	m.reportedCostDays = []time.Time{today}
	m.reportedCosts = []core.ReportedCost{{
		Provider: core.Claude, Day: today, Model: "claude-opus-4-8",
		AmountNanoUSD: 12_000_000_000, Source: "anthropic_admin_cost",
	}}
	m.rebuild()
	out := m.reconcileContent()
	if !strings.Contains(out, "RECONCILE") {
		t.Fatalf("want a reconcile heading, got:\n%s", out)
	}
	if !strings.Contains(out, "$12.00") {
		t.Fatalf("want the billed amount rendered, got:\n%s", out)
	}
	if !strings.Contains(out, "residual") {
		t.Fatalf("want a residual line, got:\n%s", out)
	}
}

func TestReconcileContentExplainsEmptyState(t *testing.T) {
	m := spendModel(t)
	m.tab = reconcileTab
	m.rebuild()
	out := m.reconcileContent()
	if !strings.Contains(out, "No overlapping days") {
		t.Fatalf("want an explicit empty state, got:\n%s", out)
	}
}

// Reconciliation cannot attribute organization billing to a filtered subset, so
// both filters must explain themselves rather than render a misleading zero.
func TestReconcileContentRefusesFilteredViews(t *testing.T) {
	m := spendModel(t)
	m.tab = reconcileTab
	m.provider = core.Codex
	if out := m.reconcileContent(); !strings.Contains(out, "Claude only") {
		t.Fatalf("want a provider-filter explanation, got:\n%s", out)
	}
	m.provider = ""
	m.search = "work"
	if out := m.reconcileContent(); !strings.Contains(out, "search is active") {
		t.Fatalf("want a search-filter explanation, got:\n%s", out)
	}
}
