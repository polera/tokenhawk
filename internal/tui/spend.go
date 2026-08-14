package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts/v2/canvas"
	"github.com/NimbleMarkets/ntcharts/v2/linechart"
	"github.com/polera/tokenhawk/internal/core"
	exporter "github.com/polera/tokenhawk/internal/export"
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
	records := m.sessionDayRecords()
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

// sessionDayRecords builds spend rows from the daily usage ledger, so a
// session's growth lands on the UTC days it was indexed rather than entirely
// on its last update. Ledger days before the window are excluded even when
// the session itself is shown. Sessions without ledger rows (an index written
// by an older Tokenhawk) keep last-update attribution.
func (m Model) sessionDayRecords() []spendRecord {
	type sessionKey struct {
		provider core.Provider
		id       string
	}
	covered := map[sessionKey]bool{}
	for _, row := range m.usageDays {
		covered[sessionKey{row.Provider, row.SessionID}] = true
	}
	shown := map[sessionKey]bool{}
	var fallback []core.Session
	for _, session := range m.shown {
		key := sessionKey{session.Provider, session.ID}
		if covered[key] {
			shown[key] = true
		} else {
			fallback = append(fallback, session)
		}
	}
	records := sessionRecords(fallback)
	var windowDay time.Time
	if !m.spendSince.IsZero() {
		y, month, d := m.spendSince.UTC().Date()
		windowDay = time.Date(y, month, d, 0, 0, 0, 0, time.UTC)
	}
	for _, row := range m.usageDays {
		if !shown[sessionKey{row.Provider, row.SessionID}] {
			continue
		}
		if !windowDay.IsZero() && row.Day.Before(windowDay) {
			continue
		}
		records = append(records, newSessionSpendRecord(row.Provider, row.SessionID, row.Day, row.Day, row.Usage))
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
	fmt.Fprintf(&b, "%s\n\n", muted.Render(fmt.Sprintf("%s  •  %d of %d sessions  •  usage counted on the UTC day it was indexed", window, len(m.shown), len(m.sessions))))
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
	b.WriteString(m.spendTrend(records))
	b.WriteString(m.spendSection("BY PROVIDER", groupRecordsByProvider(records), 0, "provider", false))
	b.WriteString(m.spendSection("BY MODEL", groupRecordsByModel(records), 0, "model", true))
	b.WriteString(m.spendSection("BY DAY (UTC)", groupRecordsByDay(records), spendDayRows, "earlier day", false))
	return b.String()
}

type spendTrendPoint struct {
	day    time.Time
	tokens int64
	cost   float64
}

type spendTrendData struct {
	points     []spendTrendPoint
	from, to   time.Time
	bucketDays int
}

func buildSpendTrend(records []spendRecord, maxPoints int, bounds ...time.Time) spendTrendData {
	if len(records) == 0 || maxPoints <= 0 {
		return spendTrendData{}
	}
	from := records[0].day.UTC().Truncate(24 * time.Hour)
	to := from
	for _, record := range records[1:] {
		day := record.day.UTC().Truncate(24 * time.Hour)
		if day.Before(from) {
			from = day
		}
		if day.After(to) {
			to = day
		}
	}
	if len(bounds) == 2 {
		boundFrom := bounds[0].UTC().Truncate(24 * time.Hour)
		boundTo := bounds[1].UTC().Truncate(24 * time.Hour)
		if boundFrom.Before(from) {
			from = boundFrom
		}
		if boundTo.After(to) {
			to = boundTo
		}
	}
	spanDays := int(to.Sub(from)/(24*time.Hour)) + 1
	bucketDays := max(1, (spanDays+maxPoints-1)/maxPoints)
	points := make([]spendTrendPoint, (spanDays+bucketDays-1)/bucketDays)
	for i := range points {
		points[i].day = from.AddDate(0, 0, i*bucketDays)
	}
	for _, record := range records {
		day := record.day.UTC().Truncate(24 * time.Hour)
		index := int(day.Sub(from)/(24*time.Hour)) / bucketDays
		points[index].tokens += record.usage.Total
		points[index].cost += record.reportedCost + record.apiRateCost
	}
	return spendTrendData{points: points, from: from, to: to, bucketDays: bucketDays}
}

func (m Model) spendTrend(records []spendRecord) string {
	width := 80
	if m.width > 0 {
		width = max(18, m.width)
	}
	maxPoints := max(2, min(64, width-14))
	var bounds []time.Time
	if !m.spendSince.IsZero() {
		bounds = []time.Time{m.spendSince, time.Now()}
	}
	trend := buildSpendTrend(records, maxPoints, bounds...)
	if len(trend.points) == 0 {
		return ""
	}
	tokens := make([]float64, len(trend.points))
	costs := make([]float64, len(trend.points))
	for i, point := range trend.points {
		tokens[i] = float64(point.tokens)
		costs[i] = point.cost
	}
	days := make([]time.Time, len(trend.points))
	for i, point := range trend.points {
		days[i] = point.day
	}
	bucketLabel := "daily totals"
	if trend.bucketDays > 1 {
		bucketLabel = fmt.Sprintf("%d-day totals", trend.bucketDays)
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("TOKEN USAGE & COST OVER TIME (UTC)"))
	if m.width == 0 || m.width >= 60 {
		b.WriteString("  " + muted.Render("· "+bucketLabel) + "\n")
	} else {
		b.WriteString("\n  " + muted.Render(bucketLabel) + "\n")
	}
	b.WriteString(spendLineChart("TOKENS", tokens, days, width, func(value float64) string {
		return human(int64(value))
	}, titleStyle))
	b.WriteString("\n")
	b.WriteString(spendLineChart("COST (USD)", costs, days, width, func(value float64) string {
		return fmt.Sprintf("$%.2f", value)
	}, good))
	b.WriteString("\n")
	return b.String()
}

func (m Model) spendExportReport(until time.Time) exporter.SpendReport {
	records := m.spendRecords()
	view := exporter.SpendView{
		WindowSpec:           m.spendSpec,
		WindowLabel:          timerange.Label(m.spendSpec),
		Until:                until.UTC(),
		Provider:             m.provider,
		Search:               m.search,
		Attribution:          "usage is attributed to the UTC day it was indexed; history indexed in one pass lands on the session's last update day; period_end is exclusive",
		TimeseriesResolution: "1 day UTC",
	}
	if !m.spendSince.IsZero() {
		since := m.spendSince.UTC()
		view.Since = &since
	}
	report := exporter.SpendReport{
		View:      view,
		Totals:    spendExportAggregate("", len(m.shown), records),
		Providers: spendExportAggregates(groupRecordsByProvider(records)),
		Models:    spendExportAggregates(groupRecordsByModel(records)),
		Days:      spendExportAggregates(groupRecordsByDay(records)),
	}
	if len(records) == 0 {
		return report
	}
	from := records[0].day.UTC().Truncate(24 * time.Hour)
	to := from
	for _, record := range records[1:] {
		day := record.day.UTC().Truncate(24 * time.Hour)
		if day.Before(from) {
			from = day
		}
		if day.After(to) {
			to = day
		}
	}
	if !m.spendSince.IsZero() {
		from = m.spendSince.UTC().Truncate(24 * time.Hour)
		to = until.UTC().Truncate(24 * time.Hour)
	}
	byDay := map[string]spendGroup{}
	for _, group := range groupRecordsByDay(records) {
		byDay[group.name] = group
	}
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		group := byDay[day.Format("2006-01-02")]
		aggregate := spendExportAggregate("", group.sessions, group.records)
		report.Timeseries = append(report.Timeseries, exporter.SpendPoint{
			PeriodStart: day,
			PeriodEnd:   day.AddDate(0, 0, 1),
			Sessions:    aggregate.Sessions,
			Usage:       aggregate.Usage,
			Cost:        aggregate.Cost,
		})
	}
	return report
}

func spendExportAggregates(groups []spendGroup) []exporter.SpendAggregate {
	out := make([]exporter.SpendAggregate, 0, len(groups))
	for _, group := range groups {
		out = append(out, spendExportAggregate(group.name, group.sessions, group.records))
	}
	return out
}

func spendExportAggregate(name string, sessions int, records []spendRecord) exporter.SpendAggregate {
	u := core.SumUsage(recordUsage(records))
	cost := spendRecordCost(records)
	return exporter.SpendAggregate{
		Name:     name,
		Sessions: sessions,
		Usage: exporter.SpendUsage{
			Input: u.Input, CachedInput: u.CachedInput, CacheCreation: u.CacheCreation,
			CacheCreation1h: u.CacheCreation1h, Output: u.Output, Reasoning: u.Reasoning,
			Tool: u.Tool, Total: u.Total,
		},
		Cost: exporter.SpendCost{
			ReportedUSD: cost.reported, APIRateUSD: cost.apiRate, TotalUSD: cost.total(),
			HasUnpricedUsage: cost.unpriced,
		},
	}
}

func spendLineChart(title string, values []float64, days []time.Time, width int, formatY func(float64) string, lineStyle lipgloss.Style) string {
	const height = 8
	peak := 0.0
	for _, value := range values {
		peak = max(peak, value)
	}
	maxY := niceChartMax(peak, 3)
	maxX := float64(max(1, len(values)-1))
	chart := linechart.New(width, height, 0, maxX, 0, maxY,
		linechart.WithXYSteps(max(1, (width-12)/2), 2),
		linechart.WithStyles(muted, muted, lineStyle),
		linechart.WithXLabelFormatter(func(_ int, _ float64) string { return "" }),
		linechart.WithYLabelFormatter(func(_ int, value float64) string {
			return formatY(value)
		}),
	)
	chart.DrawXYAxisAndLabel()
	drawTrendDateLabels(&chart, days)
	origin := chart.Origin()
	for row := 2; row < chart.GraphHeight(); row += 2 {
		y := origin.Y - row
		for x := origin.X + 1; x < width; x += 2 {
			chart.Canvas.SetRuneWithStyle(canvas.Point{X: x, Y: y}, '·', muted)
		}
	}
	points := make([]canvas.Float64Point, len(values))
	for i, value := range values {
		points[i] = canvas.Float64Point{X: float64(i), Y: value}
	}
	if len(points) == 1 {
		chart.DrawRuneWithStyle(points[0], '●', lineStyle)
	} else {
		for i := 1; i < len(points); i++ {
			chart.DrawBrailleLineWithStyle(points[i-1], points[i], lineStyle)
		}
		if len(points) <= 14 {
			for _, point := range points {
				chart.DrawRuneWithStyle(point, '●', lineStyle)
			}
		}
	}
	return "  " + titleStyle.Render(title) + "  " + muted.Render("peak "+formatY(peak)) + "\n" + chart.View() + "\n"
}

func niceChartMax(peak float64, intervals int) float64 {
	if peak <= 0 || intervals <= 0 {
		return 1
	}
	rawStep := peak / float64(intervals)
	magnitude := math.Pow(10, math.Floor(math.Log10(rawStep)))
	normalized := rawStep / magnitude
	step := 10.0
	for _, candidate := range []float64{1, 2, 2.5, 5, 10} {
		if normalized <= candidate {
			step = candidate
			break
		}
	}
	return step * magnitude * float64(intervals)
}

func drawTrendDateLabels(chart *linechart.Model, days []time.Time) {
	if len(days) == 0 {
		return
	}
	y := chart.Origin().Y + 1
	left := days[0].Format("Jan 02")
	chart.Canvas.SetStringWithStyle(canvas.Point{X: chart.Origin().X + 1, Y: y}, left, muted)
	if len(days) == 1 || chart.Width() < 28 {
		return
	}
	right := days[len(days)-1].Format("Jan 02")
	rightX := chart.Width() - len(right)
	chart.Canvas.SetStringWithStyle(canvas.Point{X: rightX, Y: y}, right, muted)
	if chart.Width() < 48 {
		return
	}
	middle := days[len(days)/2].Format("Jan 02")
	middleX := chart.Origin().X + (chart.GraphWidth()-len(middle))/2
	if middleX > chart.Origin().X+len(left)+2 && middleX+len(middle)+2 < rightX {
		chart.Canvas.SetStringWithStyle(canvas.Point{X: middleX, Y: y}, middle, muted)
	}
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
