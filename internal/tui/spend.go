package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/polera/tokenhawk/internal/core"
	"github.com/polera/tokenhawk/internal/pricing"
	"github.com/polera/tokenhawk/internal/timerange"
)

const spendTab = 2
const spendDayRows = 14

// spendGroup is one aggregation bucket: a provider, a model, or a day.
type spendGroup struct {
	name     string
	sessions int
	records  []spendRecord
}

// spendRecord retains the provider and timestamp alongside a usage row. Those
// fields are needed to identify the exact effective-dated rate behind an
// API-rate cost; a summed dollar amount alone cannot recover that provenance.
type spendRecord struct {
	provider          core.Provider
	sessionID         string
	at                time.Time
	day               time.Time
	usage             core.Usage
	apiRateCost       float64
	reportedCost      float64
	apiRateSuppressed bool
	costSource        string
}

type spendGroups struct {
	order  []string
	byName map[string]*spendGroup
}

func newSpendGroups() *spendGroups {
	return &spendGroups{byName: map[string]*spendGroup{}}
}

func (g *spendGroups) add(name string, sessions int, records ...spendRecord) {
	entry, ok := g.byName[name]
	if !ok {
		entry = &spendGroup{name: name}
		g.byName[name] = entry
		g.order = append(g.order, name)
	}
	entry.sessions += sessions
	entry.records = append(entry.records, records...)
}

func (g *spendGroups) list() []spendGroup {
	out := make([]spendGroup, 0, len(g.order))
	for _, name := range g.order {
		out = append(out, *g.byName[name])
	}
	return out
}

// sessionUsage flattens a session's own rows and its subagents' rows, which is
// the same set Session.Totals sums.
func sessionUsage(s core.Session) []core.Usage {
	out := append([]core.Usage(nil), s.Usage...)
	for _, a := range s.Subagents {
		out = append(out, a.Usage...)
	}
	return out
}

func sessionSpendRecords(s core.Session) []spendRecord {
	out := make([]spendRecord, 0, len(s.Usage))
	for _, u := range s.Usage {
		out = append(out, newSessionSpendRecord(s.Provider, s.ID, s.UpdatedAt, s.UpdatedAt, u))
	}
	for _, a := range s.Subagents {
		for _, u := range a.Usage {
			out = append(out, newSessionSpendRecord(s.Provider, s.ID, a.UpdatedAt, s.UpdatedAt, u))
		}
	}
	return out
}

func newSessionSpendRecord(provider core.Provider, sessionID string, at, day time.Time, usage core.Usage) spendRecord {
	record := spendRecord{provider: provider, sessionID: sessionID, at: at, day: day, usage: usage}
	if usage.PricingStatus == "reported" {
		record.reportedCost = usage.CostUSD
	} else {
		record.apiRateCost = usage.CostUSD
	}
	return record
}

func recordUsage(records []spendRecord) []core.Usage {
	out := make([]core.Usage, 0, len(records))
	for _, record := range records {
		out = append(out, record.usage)
	}
	return out
}

func groupByProvider(sessions []core.Session) []spendGroup {
	return groupRecordsByProvider(sessionRecords(sessions))
}

func groupRecordsByProvider(records []spendRecord) []spendGroup {
	g := newSpendGroups()
	seen := map[string]bool{}
	for _, record := range records {
		key := string(record.provider) + "\x00" + record.sessionID
		sessions := 0
		if record.sessionID != "" && !seen[key] {
			seen[key] = true
			sessions = 1
		}
		g.add(string(record.provider), sessions, record)
	}
	out := g.list()
	sortByCost(out)
	return out
}

func groupByModel(sessions []core.Session) []spendGroup {
	return groupRecordsByModel(sessionRecords(sessions))
}

func groupRecordsByModel(records []spendRecord) []spendGroup {
	g := newSpendGroups()
	seen := map[string]bool{}
	for _, record := range records {
		name := record.usage.Model
		if name == "" {
			name = "anthropic other services"
		}
		key := name + "\x00" + record.sessionID
		sessions := 0
		if record.sessionID != "" && !seen[key] {
			seen[key] = true
			sessions = 1
		}
		g.add(name, sessions, record)
	}
	out := g.list()
	sortByCost(out)
	return out
}

// groupByDay buckets each session by the day it was last updated. Provider
// stores keep only per-session running totals, so a session's whole usage lands
// on that one day rather than being spread across the days it ran.
func groupByDay(sessions []core.Session) []spendGroup {
	return groupRecordsByDay(sessionRecords(sessions))
}

func groupRecordsByDay(records []spendRecord) []spendGroup {
	g := newSpendGroups()
	seen := map[string]bool{}
	for _, record := range records {
		name := record.day.UTC().Format("2006-01-02")
		key := name + "\x00" + record.sessionID
		sessions := 0
		if record.sessionID != "" && !seen[key] {
			seen[key] = true
			sessions = 1
		}
		g.add(name, sessions, record)
	}
	out := g.list()
	sort.SliceStable(out, func(i, j int) bool { return out[i].name > out[j].name })
	return out
}

func sortByCost(groups []spendGroup) {
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := spendRecordCost(groups[i].records), spendRecordCost(groups[j].records)
		if a.total() != b.total() {
			return a.total() > b.total()
		}
		usageA, usageB := core.SumUsage(recordUsage(groups[i].records)), core.SumUsage(recordUsage(groups[j].records))
		return usageA.Total > usageB.Total
	})
}

func sessionRecords(sessions []core.Session) []spendRecord {
	var out []spendRecord
	for _, session := range sessions {
		out = append(out, sessionSpendRecords(session)...)
	}
	return out
}

// spendWindow resolves the model's window spec, tolerating a spec that no
// longer parses so the view can report it instead of rendering nothing.
func (m Model) spendWindow() (time.Time, string) {
	return m.spendSince, timerange.Label(m.spendSpec)
}

type spendCost struct {
	reported float64
	apiRate  float64
	unpriced bool
}

func (c spendCost) total() float64 { return c.reported + c.apiRate }

func spendRecordCost(records []spendRecord) spendCost {
	var out spendCost
	for _, record := range records {
		out.reported += record.reportedCost
		out.apiRate += record.apiRateCost
		if record.usage.PricingStatus != "priced" && record.usage.PricingStatus != "reported" && record.usage.Total > 0 {
			out.unpriced = true
		}
	}
	return out
}

// spendRecords combines local token-attribution rows with the Anthropic daily
// billing ledger. Claude API-rate costs are suppressed only on UTC days whose
// successful API fetch is recorded in reportedCostDays.
func (m Model) spendRecords() []spendRecord {
	records := sessionRecords(m.shown)
	if m.provider != "" && m.provider != core.Claude || m.search != "" {
		return records
	}
	covered := map[string]bool{}
	for _, day := range m.reportedCostDays {
		if m.reportedDayInWindow(day) {
			covered[day.UTC().Format("2006-01-02")] = true
		}
	}
	for i := range records {
		if records[i].provider != core.Claude || !covered[records[i].day.UTC().Format("2006-01-02")] {
			continue
		}
		records[i].apiRateCost = 0
		records[i].apiRateSuppressed = true
	}
	for _, cost := range m.reportedCosts {
		if cost.Provider != core.Claude || !m.reportedDayInWindow(cost.Day) {
			continue
		}
		records = append(records, spendRecord{
			provider:     core.Claude,
			at:           cost.Day,
			day:          cost.Day,
			usage:        core.Usage{Model: cost.Model, PricingStatus: "reported"},
			reportedCost: cost.USD(),
			costSource:   cost.Source,
		})
	}
	return records
}

func (m Model) reportedDayInWindow(day time.Time) bool {
	if m.spendSince.IsZero() {
		return true
	}
	y, month, d := m.spendSince.UTC().Date()
	first := time.Date(y, month, d, 0, 0, 0, 0, time.UTC)
	return !day.UTC().Before(first)
}

func (m Model) spendContent() string {
	since, label := m.spendWindow()
	var b strings.Builder
	window := "all recorded sessions"
	if !since.IsZero() {
		window = since.Format("2006-01-02 15:04") + " → now"
	}
	b.WriteString(titleStyle.Render("SPEND · "+label) + "\n")
	fmt.Fprintf(&b, "%s\n\n", muted.Render(fmt.Sprintf("%s  •  %d of %d sessions  •  counted by last session update", window, len(m.shown), len(m.sessions))))
	records := m.spendRecords()
	if len(records) == 0 {
		b.WriteString(muted.Render("No sessions were updated in this window. Press t for another range or d to set one.") + "\n")
		return b.String()
	}
	total := core.SumUsage(recordUsage(records))
	fmt.Fprintf(&b, "%s  tokens %s  in %s  cached %s  out %s  i:o %s\n", titleStyle.Render("TOTAL"), human(total.Total), human(total.Input), cachedText(total), human(total.Output), ratioText(total.Input, total.Output))
	fmt.Fprintf(&b, "        %s\n", spendCostDetail(spendRecordCost(records)))
	if note := m.reportedCostNote(); note != "" {
		fmt.Fprintf(&b, "        %s\n", muted.Render(note))
	}
	if cacheAlarm(total) {
		fmt.Fprintf(&b, "%s\n", cacheAlarmText("this window", total))
	}
	b.WriteString("\n")
	b.WriteString(m.spendSection("BY PROVIDER", groupRecordsByProvider(records), 0, "provider", false))
	b.WriteString(m.spendSection("BY MODEL", groupRecordsByModel(records), 0, "model", true))
	b.WriteString(m.spendSection("BY DAY (UTC)", groupRecordsByDay(records), spendDayRows, "earlier day", false))
	return b.String()
}

func (m Model) reportedCostNote() string {
	if m.search != "" && len(m.reportedCostDays) > 0 {
		return "Anthropic billing is excluded while search is active because API costs cannot be attributed to local projects or sessions."
	}
	if m.provider != "" && m.provider != core.Claude {
		return ""
	}
	coverage := 0
	for _, day := range m.reportedCostDays {
		if m.reportedDayInWindow(day) {
			coverage++
		}
	}
	if coverage > 0 {
		return fmt.Sprintf("Anthropic Admin API organization billing covers %d UTC day(s); overlapping Claude API-rate costs are excluded.", coverage)
	}
	return ""
}

func spendUsage(sessions []core.Session) []core.Usage {
	var out []core.Usage
	for _, s := range sessions {
		out = append(out, sessionUsage(s)...)
	}
	return out
}

// spendSection renders one aggregation with a cost bar scaled to the largest
// row. limit caps the rows and reports the remainder; 0 means no cap.
func (m Model) spendSection(title string, groups []spendGroup, limit int, noun string, showPricing bool) string {
	if len(groups) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render(title) + "\n")
	peak := 0.0
	for _, g := range groups {
		if weight := spendRecordWeight(g.records); weight > peak {
			peak = weight
		}
	}
	nameWidth := 0
	for _, g := range groups {
		if n := len([]rune(g.name)); n > nameWidth {
			nameWidth = n
		}
	}
	nameWidth = min(nameWidth, 28)
	shown := groups
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	for _, g := range shown {
		u := core.SumUsage(recordUsage(g.records))
		cost := spendRecordCost(g.records)
		name := g.name
		if r := []rune(name); len(r) > nameWidth {
			name = string(r[:nameWidth-1]) + "…"
		}
		// The bar and the muted styles inside it carry their own resets, so an
		// alarm is marked with a leading glyph rather than by styling the line.
		prefix := "  "
		if cacheAlarm(u) {
			prefix = alarmStyle.Render("⚠") + " "
		}
		line := prefix + fmt.Sprintf("%-*s %s %5d sess  tokens %8s", nameWidth, name, spendBar(spendRecordWeight(g.records), peak, m.width), g.sessions, human(u.Total))
		if m.width >= 96 {
			line += fmt.Sprintf("  in %8s  cached %6s  out %8s", human(u.Input), human(u.CachedInput), human(u.Output))
		}
		line += fmt.Sprintf("  i:o %s", ratioText(u.Input, u.Output))
		line += "  " + spendCostText(cost)
		b.WriteString(line + "\n")
		if showPricing {
			for _, detail := range m.modelPricingDetails(g.records) {
				b.WriteString(muted.Render("      "+detail) + "\n")
			}
		}
	}
	if rest := len(groups) - len(shown); rest > 0 {
		b.WriteString(muted.Render(fmt.Sprintf("  … %d more %s(s) not shown", rest, noun)) + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

type spendRateGroup struct {
	rate     pricing.Rate
	provider core.Provider
	usage    []core.Usage
}

// modelPricingDetails explains priced model rows in the same units the catalog
// uses. Multiple lines are possible when a window crosses an effective-date
// change or the same model identifier appears under different providers.
func (m Model) modelPricingDetails(records []spendRecord) []string {
	if m.prices == nil {
		return nil
	}
	var order []string
	groups := map[string]*spendRateGroup{}
	for _, record := range records {
		if record.usage.PricingStatus != "priced" || record.apiRateSuppressed {
			continue
		}
		rate, ok := m.prices.Lookup(record.provider, record.at, record.usage.Model)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s\x00%s\x00%s", record.provider, rate.Model, rate.EffectiveFrom)
		group, exists := groups[key]
		if !exists {
			group = &spendRateGroup{rate: rate, provider: record.provider}
			groups[key] = group
			order = append(order, key)
		}
		group.usage = append(group.usage, record.usage)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return groups[order[i]].rate.EffectiveFrom < groups[order[j]].rate.EffectiveFrom
	})
	out := make([]string, 0, len(order)*2)
	for _, key := range order {
		group := groups[key]
		u := core.SumUsage(group.usage)
		input := max(int64(0), u.Input-u.CachedInput)
		output := u.Output
		outputLabel := "output"
		if group.rate.Provider == core.Gemini && u.Reasoning > 0 {
			output += u.Reasoning
			outputLabel = "output + reasoning"
		}
		long := min(u.CacheCreation1h, u.CacheCreation)
		short := u.CacheCreation - long
		out = append(out, fmt.Sprintf("API rate: %s input × %s/M  +  %s cached input × %s/M",
			human(input), dollarRate(group.rate.Input), human(u.CachedInput), dollarRate(group.rate.CachedInput)))
		// The two cache-write tiers bill differently, so only collapse them into
		// one term when nothing was written at the 1-hour rate.
		cacheTerms := fmt.Sprintf("%s cache write × %s/M", human(u.CacheCreation), dollarRate(group.rate.CacheCreation))
		if long > 0 {
			cacheTerms = fmt.Sprintf("%s cache write 5m × %s/M  +  %s cache write 1h × %s/M",
				human(short), dollarRate(group.rate.CacheCreation), human(long), dollarRate(group.rate.LongCacheCreation()))
			out = append(out, "          "+cacheTerms)
			cacheTerms = ""
		}
		terms := fmt.Sprintf("          %s %s × %s/M", human(output), outputLabel, dollarRate(group.rate.Output))
		if cacheTerms != "" {
			terms = fmt.Sprintf("          %s  +  %s %s × %s/M", cacheTerms, human(output), outputLabel, dollarRate(group.rate.Output))
		}
		summary := fmt.Sprintf("$%.6f  ·  %s rate effective %s", group.rate.Cost(u), group.provider, group.rate.EffectiveFrom)
		if m.width > 0 && m.width < 120 {
			out = append(out, terms, "          =  "+summary)
		} else {
			out = append(out, terms+"  =  "+summary)
		}
	}
	reported := 0.0
	for _, record := range records {
		if record.costSource != "" {
			reported += record.reportedCost
		}
	}
	if reported != 0 {
		out = append([]string{fmt.Sprintf("reported: $%.6f  ·  Anthropic Admin Cost API", reported)}, out...)
	}
	return out
}

func dollarRate(rate float64) string {
	return "$" + compactRate(rate)
}

func compactRate(rate float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", rate), "0"), ".")
}

// spendWeight ranks a bucket by cost, falling back to tokens so unpriced
// providers still produce a readable bar.
func spendRecordWeight(records []spendRecord) float64 {
	if cost := spendRecordCost(records).total(); cost > 0 {
		return cost
	}
	u := core.SumUsage(recordUsage(records))
	return float64(u.Total)
}

func spendCostText(cost spendCost) string {
	switch {
	case cost.reported != 0 && cost.apiRate != 0:
		return fmt.Sprintf("$%.4f reported + $%.4f API rate", cost.reported, cost.apiRate)
	case cost.reported != 0:
		return fmt.Sprintf("$%.4f reported", cost.reported)
	case cost.apiRate != 0 && cost.unpriced:
		return fmt.Sprintf("$%.4f+ API rate", cost.apiRate)
	case cost.apiRate != 0:
		return fmt.Sprintf("$%.4f API rate", cost.apiRate)
	case cost.unpriced:
		return "unpriced"
	default:
		return "$0.0000"
	}
}

func spendCostDetail(cost spendCost) string {
	switch {
	case cost.reported != 0 && cost.apiRate != 0:
		suffix := ""
		if cost.unpriced {
			suffix = " + unpriced usage"
		}
		return fmt.Sprintf("$%.6f reported + $%.6f API rate%s", cost.reported, cost.apiRate, suffix)
	case cost.reported != 0:
		if cost.unpriced {
			return fmt.Sprintf("$%.6f reported + unpriced usage", cost.reported)
		}
		return fmt.Sprintf("$%.6f reported", cost.reported)
	case cost.apiRate != 0 && cost.unpriced:
		return fmt.Sprintf("$%.6f+ API rate (partially priced)", cost.apiRate)
	case cost.apiRate != 0:
		return fmt.Sprintf("$%.6f API rate", cost.apiRate)
	case cost.unpriced:
		return "unpriced"
	default:
		return "$0.000000 reported"
	}
}

func spendBar(weight, peak float64, width int) string {
	size := 12
	if width < 96 {
		size = 6
	}
	if peak <= 0 || weight <= 0 {
		return muted.Render(strings.Repeat("·", size))
	}
	filled := int(weight / peak * float64(size))
	filled = min(max(filled, 1), size)
	return strings.Repeat("█", filled) + muted.Render(strings.Repeat("·", size-filled))
}

func cachedText(u core.Usage) string {
	if u.Input <= 0 {
		return human(u.CachedInput)
	}
	return fmt.Sprintf("%s (%.0f%%)", human(u.CachedInput), float64(u.CachedInput)/float64(u.Input)*100)
}
