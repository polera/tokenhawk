package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/polera/tokenhawk/internal/core"
)

// reconcileRow compares one UTC day and model between the local list-price
// estimate and Anthropic's reported billing. Both sides are kept so the view
// can show the residual rather than only a signed percentage, which hides the
// magnitude on days with very little spend.
type reconcileRow struct {
	day       time.Time
	model     string
	estimated float64
	reported  float64
}

func (r reconcileRow) residual() float64 { return r.estimated - r.reported }

// driftRatio expresses the residual as a fraction of reported spend. It is only
// meaningful when reported spend is non-trivial; callers gate on hasRatio.
func (r reconcileRow) driftRatio() float64 { return r.residual() / r.reported }

// hasRatio reports whether reported spend is large enough for a percentage to
// carry information. Below this a cent of rounding reads as a huge percentage.
func (r reconcileRow) hasRatio() bool { return r.reported >= 0.01 }

// reconcileRows pairs suppressed local estimates with reported billing amounts.
// Only days that Anthropic actually covered produce rows: an uncovered day has
// no reported side to compare against, and emitting it would read as 100% drift
// rather than "not yet fetched".
func reconcileRows(records []spendRecord) []reconcileRow {
	type key struct {
		day   string
		model string
	}
	index := map[key]*reconcileRow{}
	get := func(day time.Time, model string) *reconcileRow {
		utc := day.UTC()
		k := key{day: utc.Format("2006-01-02"), model: model}
		if row, ok := index[k]; ok {
			return row
		}
		row := &reconcileRow{
			day:   time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC),
			model: model,
		}
		index[k] = row
		return row
	}
	for _, record := range records {
		if record.provider != core.Claude {
			continue
		}
		switch {
		case record.estimateSuppressed:
			get(record.day, record.usage.Model).estimated += record.suppressedEstimate
		case record.reportedCost != 0:
			get(record.day, record.usage.Model).reported += record.reportedCost
		}
	}
	out := make([]reconcileRow, 0, len(index))
	for _, row := range index {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].day.Equal(out[j].day) {
			return out[i].day.After(out[j].day)
		}
		if out[i].residualMagnitude() != out[j].residualMagnitude() {
			return out[i].residualMagnitude() > out[j].residualMagnitude()
		}
		return out[i].model < out[j].model
	})
	return out
}

func (r reconcileRow) residualMagnitude() float64 {
	if d := r.residual(); d < 0 {
		return -d
	} else {
		return d
	}
}

// reconcileTotals sums both sides across every row.
func reconcileTotals(rows []reconcileRow) reconcileRow {
	var out reconcileRow
	for _, row := range rows {
		out.estimated += row.estimated
		out.reported += row.reported
	}
	return out
}

const reconcileRowLimit = 20

// reconcileContent renders the estimate-vs-billed comparison. It is the
// measurement surface for pricing accuracy: rather than reasoning about whether
// the catalog is right, this shows the residual against authoritative billing.
func (m Model) reconcileContent() string {
	var b strings.Builder
	_, label := m.spendWindow()
	b.WriteString(titleStyle.Render("RECONCILE · "+label) + "\n")
	b.WriteString(muted.Render("Local list-price estimate vs Anthropic Admin API billing, per UTC day and model.") + "\n\n")

	if m.provider != "" && m.provider != core.Claude {
		b.WriteString(muted.Render("Reconciliation applies to Claude only. Clear the provider filter to compare.") + "\n")
		return b.String()
	}
	if m.search != "" {
		b.WriteString(muted.Render("Reconciliation is unavailable while search is active because billing cannot be attributed to individual sessions.") + "\n")
		return b.String()
	}

	rows := reconcileRows(m.spendRecords())
	if len(rows) == 0 {
		b.WriteString(muted.Render("No overlapping days yet. Reconciliation needs an Anthropic admin key and at least one covered UTC day.") + "\n")
		return b.String()
	}

	total := reconcileTotals(rows)
	fmt.Fprintf(&b, "%s  estimated %s  billed %s  residual %s\n",
		titleStyle.Render("TOTAL"),
		dollars(total.estimated), dollars(total.reported), signedDollars(total.residual()))
	if total.hasRatio() {
		fmt.Fprintf(&b, "        %s\n", muted.Render(fmt.Sprintf("estimate is %s vs billed", signedPercent(total.driftRatio()))))
	}
	b.WriteString("\n")

	shown := rows
	if len(shown) > reconcileRowLimit {
		shown = shown[:reconcileRowLimit]
	}
	// Pad before styling: muted.Render wraps the text in ANSI escapes, which
	// count toward a %-12s width and would misalign the header over its column.
	fmt.Fprintf(&b, "%s\n", muted.Render(fmt.Sprintf("%-12s %-26s %10s %10s %11s %9s",
		"DAY", "MODEL", "EST", "BILLED", "RESIDUAL", "DRIFT")))
	for _, row := range shown {
		model := row.model
		if model == "" {
			model = "(unattributed)"
		}
		if len(model) > 26 {
			model = model[:25] + "…"
		}
		drift := "—"
		if row.hasRatio() {
			drift = signedPercent(row.driftRatio())
		}
		fmt.Fprintf(&b, "%-12s %-26s %10s %10s %11s %9s\n",
			row.day.Format("2006-01-02"), model,
			dollars(row.estimated), dollars(row.reported),
			signedDollars(row.residual()), drift)
	}
	if len(rows) > len(shown) {
		fmt.Fprintf(&b, "%s\n", muted.Render(fmt.Sprintf("… and %d more row(s)", len(rows)-len(shown))))
	}
	b.WriteString("\n")
	if missing := unpricedModels(m.spendRecords()); len(missing) > 0 {
		fmt.Fprintf(&b, "%s\n", alarmStyle.Render(fmt.Sprintf("⚠ %d model(s) with usage but no catalog rate: %s",
			len(missing), strings.Join(missing, ", "))))
		b.WriteString(muted.Render("Add these to internal/pricing/catalog.json, or override with --pricing.") + "\n")
	}
	b.WriteString(muted.Render("A model billed but never estimated locally is usually missing from the pricing catalog.") + "\n")
	return b.String()
}

// unpricedModels lists models that recorded usage the catalog could not price.
// Price() marks these "unpriced" and returns a zero cost, so without an explicit
// callout they read as free rather than as a missing rate.
func unpricedModels(records []spendRecord) []string {
	seen := map[string]bool{}
	for _, record := range records {
		u := record.usage
		if u.PricingStatus != "unpriced" || u.Total == 0 || u.Model == "" {
			continue
		}
		seen[u.Model] = true
	}
	out := make([]string, 0, len(seen))
	for model := range seen {
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

func dollars(v float64) string { return fmt.Sprintf("$%.2f", v) }

func signedDollars(v float64) string {
	if v >= 0 {
		return fmt.Sprintf("+$%.2f", v)
	}
	return fmt.Sprintf("-$%.2f", -v)
}

func signedPercent(ratio float64) string {
	return fmt.Sprintf("%+.1f%%", ratio*100)
}
