package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/polera/tokenhawk/internal/core"
	"github.com/polera/tokenhawk/internal/timerange"
)

const spendTab = 3
const spendDayRows = 14

// spendGroup is one aggregation bucket: a provider, a model, or a day.
type spendGroup struct {
	name     string
	sessions int
	usage    []core.Usage
}

type spendGroups struct {
	order  []string
	byName map[string]*spendGroup
}

func newSpendGroups() *spendGroups {
	return &spendGroups{byName: map[string]*spendGroup{}}
}

func (g *spendGroups) add(name string, sessions int, usage ...core.Usage) {
	entry, ok := g.byName[name]
	if !ok {
		entry = &spendGroup{name: name}
		g.byName[name] = entry
		g.order = append(g.order, name)
	}
	entry.sessions += sessions
	entry.usage = append(entry.usage, usage...)
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

func groupByProvider(sessions []core.Session) []spendGroup {
	g := newSpendGroups()
	for _, s := range sessions {
		g.add(string(s.Provider), 1, sessionUsage(s)...)
	}
	out := g.list()
	sortByCost(out)
	return out
}

func groupByModel(sessions []core.Session) []spendGroup {
	g := newSpendGroups()
	for _, s := range sessions {
		seen := map[string]bool{}
		for _, u := range sessionUsage(s) {
			name := u.Model
			if name == "" {
				name = "unknown"
			}
			sessions := 0
			if !seen[name] {
				seen[name] = true
				sessions = 1
			}
			g.add(name, sessions, u)
		}
	}
	out := g.list()
	sortByCost(out)
	return out
}

// groupByDay buckets each session by the day it was last updated. Provider
// stores keep only per-session running totals, so a session's whole usage lands
// on that one day rather than being spread across the days it ran.
func groupByDay(sessions []core.Session) []spendGroup {
	g := newSpendGroups()
	for _, s := range sessions {
		g.add(s.UpdatedAt.Local().Format("2006-01-02"), 1, sessionUsage(s)...)
	}
	out := g.list()
	sort.SliceStable(out, func(i, j int) bool { return out[i].name > out[j].name })
	return out
}

func sortByCost(groups []spendGroup) {
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := core.SumUsage(groups[i].usage), core.SumUsage(groups[j].usage)
		if a.CostUSD != b.CostUSD {
			return a.CostUSD > b.CostUSD
		}
		return a.Total > b.Total
	})
}

// spendWindow resolves the model's window spec, tolerating a spec that no
// longer parses so the view can report it instead of rendering nothing.
func (m Model) spendWindow() (time.Time, string) {
	return m.spendSince, timerange.Label(m.spendSpec)
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
	if len(m.shown) == 0 {
		b.WriteString(muted.Render("No sessions were updated in this window. Press t for another range or d to set one.") + "\n")
		return b.String()
	}
	total := core.SumUsage(spendUsage(m.shown))
	fmt.Fprintf(&b, "%s  tokens %s  in %s  cached %s  out %s  i:o %s\n", titleStyle.Render("TOTAL"), human(total.Total), human(total.Input), cachedText(total), human(total.Output), ratioText(total.Input, total.Output))
	fmt.Fprintf(&b, "        %s\n", costDetail(total))
	if cacheAlarm(total) {
		fmt.Fprintf(&b, "%s\n", cacheAlarmText("this window", total))
	}
	b.WriteString("\n")
	b.WriteString(m.spendSection("BY PROVIDER", groupByProvider(m.shown), 0, "provider"))
	b.WriteString(m.spendSection("BY MODEL", groupByModel(m.shown), 0, "model"))
	b.WriteString(m.spendSection("BY DAY", groupByDay(m.shown), spendDayRows, "earlier day"))
	return b.String()
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
func (m Model) spendSection(title string, groups []spendGroup, limit int, noun string) string {
	if len(groups) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render(title) + "\n")
	peak := 0.0
	for _, g := range groups {
		if weight := spendWeight(core.SumUsage(g.usage)); weight > peak {
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
		u := core.SumUsage(g.usage)
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
		line := prefix + fmt.Sprintf("%-*s %s %5d sess  tokens %8s", nameWidth, name, spendBar(spendWeight(u), peak, m.width), g.sessions, human(u.Total))
		if m.width >= 96 {
			line += fmt.Sprintf("  in %8s  cached %6s  out %8s", human(u.Input), human(u.CachedInput), human(u.Output))
		}
		line += fmt.Sprintf("  i:o %s", ratioText(u.Input, u.Output))
		line += "  " + costText(u)
		b.WriteString(line + "\n")
	}
	if rest := len(groups) - len(shown); rest > 0 {
		b.WriteString(muted.Render(fmt.Sprintf("  … %d more %s(s) not shown", rest, noun)) + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// spendWeight ranks a bucket by cost, falling back to tokens so unpriced
// providers still produce a readable bar.
func spendWeight(u core.Usage) float64 {
	if u.CostUSD > 0 {
		return u.CostUSD
	}
	return float64(u.Total)
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
