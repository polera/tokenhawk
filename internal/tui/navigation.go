package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"
	"github.com/polera/tokenhawk/internal/core"
	"github.com/polera/tokenhawk/internal/monitor"
	"github.com/polera/tokenhawk/internal/timerange"
)

// The visual system deliberately uses a restrained cyan/slate palette. Strong
// colors are reserved for focus, live state, and conditions that need action.
var (
	brandStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#22D3EE"))
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#22D3EE"))
	muted      = lipgloss.NewStyle().Foreground(lipgloss.Color("#7C8799"))
	subtle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#A8B2C1"))
	good       = lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399"))
	alarmStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FB7185"))
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBF24"))
	keyStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#67E8F9"))

	navActive = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("#062630")).
			Background(lipgloss.Color("#22D3EE")).
			Padding(0, 1)
	navIdle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A8B2C1")).Padding(0, 1)

	panelTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E2E8F0"))
)

type keyHint struct {
	key   string
	label string
}

func tableTheme(dark bool) table.Styles {
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#94A3B8")).Padding(0, 1)
	cell := lipgloss.NewStyle().Foreground(lipgloss.Color("#CBD5E1")).Padding(0, 1)
	selected := lipgloss.NewStyle().Bold(true).
		BorderLeft(true).
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(lipgloss.Color("#22D3EE"))
	if !dark {
		header = header.Foreground(lipgloss.Color("#475569"))
		cell = cell.Foreground(lipgloss.Color("#1E293B"))
		selected = selected.BorderForeground(lipgloss.Color("#0891B2"))
	}
	return table.Styles{Header: header, Cell: cell, Selected: selected}
}

func statusStyle(active bool) lipgloss.Style {
	if active {
		return good.Bold(true)
	}
	return muted
}

func (m *Model) navigateView(delta int) {
	const viewCount = transcriptTab + 1
	next := (m.tab + delta + viewCount) % viewCount
	m.notice = ""
	m.spendOffset = 0
	if next == transcriptTab {
		m.openTranscriptSearch()
		return
	}
	m.tab = next
	m.rebuild()
}

func (m Model) dashboardChrome() (string, string) {
	active, runningAgents := 0, 0
	for _, session := range m.sessions {
		if session.Active {
			active++
		}
		runningAgents += session.RunningSubagents()
	}

	var status monitor.Status
	if m.monitor != nil {
		status = m.monitor.Status()
	}

	brand := brandStyle.Render("TOKENHAWK")
	if m.width >= 60 || m.width <= 0 {
		brand += "  " + muted.Render("TOKEN OPERATIONS")
	}
	activity := good.Render("● LIVE") + subtle.Render(fmt.Sprintf("  %d active  ·  %d history  ·  %d agents", active, len(m.sessions)-active, runningAgents))
	header := joinAcross(brand, activity, m.width)
	header += "\n" + renderNavigation(m.tab, m.width)
	header += "\n" + m.contextLine()

	if alarms := activeCacheAlarms(m.sessions); alarms > 0 {
		header += "\n" + alarmStyle.Render(fmt.Sprintf("▲ %d high-input session(s) need cache attention", alarms))
	}
	if prompt := m.inputLine(); prompt != "" {
		header += "\n" + prompt
	}

	footer := renderHints(m.footerHints(), m.width)
	indexStatus := fmt.Sprintf("INDEX  %d files", status.Files)
	if status.Scanning {
		indexStatus += "  ·  syncing"
	}
	footer += "\n" + muted.Render(indexStatus)
	if status.Warning != "" {
		footer += "  " + warnStyle.Render("▲ "+status.Warning)
	}
	if status.CostWarning != "" {
		footer += "  " + warnStyle.Render("▲ "+status.CostWarning)
	}
	if m.notice != "" {
		noticeStyle := good
		lower := strings.ToLower(m.notice)
		if strings.Contains(lower, "fail") || strings.Contains(lower, "error") || strings.Contains(lower, "unavailable") {
			noticeStyle = alarmStyle
		}
		footer += "\n" + noticeStyle.Render(m.notice)
	}
	return header, footer
}

func (m Model) contextLine() string {
	titles := []string{"LIVE SESSIONS", "SESSION HISTORY", "SPEND ANALYSIS", "TRANSCRIPT SEARCH"}
	title := titles[min(max(0, m.tab), len(titles)-1)]
	count := fmt.Sprintf("%d / %d visible", len(m.shown), len(m.sessions))
	if m.tab == transcriptTab {
		count = fmt.Sprintf("%d transcript matches", len(m.transcriptReport.Matches))
	}
	left := panelTitle.Render(title) + muted.Render("  ·  "+count)

	provider := "ALL PROVIDERS"
	if m.provider != "" {
		provider = strings.ToUpper(string(m.provider))
	}
	sortName := strings.ToUpper([]string{"UPDATED", "TOKENS", "COST"}[m.sortMode])
	right := muted.Render("PROVIDER ") + subtle.Render(provider)
	if m.tab != transcriptTab {
		right += muted.Render("   SORT ") + subtle.Render(sortName)
	}
	if m.tab == spendTab {
		right += muted.Render("   WINDOW ") + subtle.Render(strings.ToUpper(timerange.Label(m.spendSpec)))
	}
	if m.width > 0 && lipgloss.Width(left)+lipgloss.Width(right)+3 > m.width {
		return left + "\n" + right
	}
	return joinAcross(left, right, m.width)
}

func (m Model) inputLine() string {
	switch {
	case m.transcriptInput:
		return keyStyle.Render("SEARCH") + "  " + m.transcriptDraft + titleStyle.Render("▌")
	case m.tab == transcriptTab && m.transcriptQuery != "":
		return muted.Render("query: " + m.transcriptQuery)
	case m.sinceInput:
		return keyStyle.Render("SINCE") + "  " + m.sinceDraft + titleStyle.Render("▌")
	case m.searching:
		return keyStyle.Render("FILTER") + "  " + m.search + titleStyle.Render("▌")
	case m.search != "":
		return muted.Render("FILTER") + "  " + m.search + "  " + keyStyle.Render("× esc")
	default:
		return ""
	}
}

func (m Model) footerHints() []keyHint {
	switch m.tab {
	case spendTab:
		return []keyHint{{"↑↓", "scroll"}, {"t", "range"}, {"d", "since"}, {"e", "JSON"}, {"x", "CSV"}, {"p", "provider"}, {"Tab", "next view"}, {"?", "shortcuts"}, {"q", "quit"}}
	case transcriptTab:
		return []keyHint{{"↑↓", "select"}, {"Enter", "open"}, {"/", "query"}, {"r", "refresh"}, {"p", "provider"}, {"Tab", "next view"}, {"?", "shortcuts"}, {"q", "quit"}}
	default:
		return []keyHint{{"↑↓", "select"}, {"Enter", "details"}, {"/", "filter"}, {"p", "provider"}, {"s", "sort"}, {"Tab", "next view"}, {"?", "shortcuts"}, {"q", "quit"}}
	}
}

func dashboardTabs(width int) []string {
	if width > 0 && width < 68 {
		return []string{"1A", "2I", "3$", "4?"}
	}
	return []string{"1 Live", "2 History", "3 Spend", "4 Search"}
}

func renderNavigation(active, width int) string {
	tabs := dashboardTabs(width)
	compact := width > 0 && width < 68
	for i, tab := range tabs {
		if i == active {
			if compact {
				tabs[i] = titleStyle.Bold(true).Render(tab)
			} else {
				tabs[i] = navActive.Render(tab)
			}
		} else if compact {
			tabs[i] = muted.Render(tab)
		} else {
			tabs[i] = navIdle.Render(tab)
		}
	}
	separator := "  "
	if compact {
		separator = " "
	}
	return strings.Join(tabs, separator)
}

func renderHints(hints []keyHint, width int) string {
	parts := make([]string, 0, len(hints))
	used := 0
	for _, hint := range hints {
		plainWidth := lipgloss.Width(hint.key) + 1 + lipgloss.Width(hint.label)
		separatorWidth := 5
		if len(parts) > 0 && width > 0 && used+separatorWidth+plainWidth > width {
			break
		}
		parts = append(parts, keyStyle.Render(hint.key)+" "+muted.Render(hint.label))
		used += plainWidth
		if len(parts) > 1 {
			used += separatorWidth
		}
	}
	return strings.Join(parts, muted.Render("  ·  "))
}

func joinAcross(left, right string, width int) string {
	if width <= 0 {
		return left + "  " + right
	}
	space := width - lipgloss.Width(left) - lipgloss.Width(right)
	if space < 2 {
		return left
	}
	return left + strings.Repeat(" ", space) + right
}

func (m Model) helpView() string {
	var body strings.Builder
	body.WriteString(brandStyle.Render("TOKENHAWK") + "  " + muted.Render("KEYBOARD REFERENCE") + "\n\n")
	body.WriteString(panelTitle.Render("NAVIGATION") + "\n")
	body.WriteString(shortcutRow("1–4", "Open a view directly") + "\n")
	body.WriteString(shortcutRow("Tab / Shift+Tab", "Move between views") + "\n")
	body.WriteString(shortcutRow("↑↓ or j/k", "Move selection or scroll") + "\n")
	body.WriteString(shortcutRow("Enter", "Open the selected session") + "\n\n")

	body.WriteString(panelTitle.Render("REFINE") + "\n")
	body.WriteString(shortcutRow("/", "Filter sessions or search visible content") + "\n")
	body.WriteString(shortcutRow("p", "Cycle provider") + "\n")
	body.WriteString(shortcutRow("s", "Cycle session sorting") + "\n")
	body.WriteString(shortcutRow("t / d", "Cycle or type a spend window") + "\n\n")

	body.WriteString(panelTitle.Render("ACTIONS") + "\n")
	body.WriteString(shortcutRow("e / x", "Export JSON or CSV") + "\n")
	body.WriteString(shortcutRow("PgUp / PgDn", "Move one page") + "\n")
	body.WriteString(shortcutRow("g / G", "Jump to first or last item") + "\n")
	body.WriteString(shortcutRow("q", "Quit Tokenhawk") + "\n\n")
	body.WriteString(muted.Render("Press any key to return"))
	return body.String()
}

func shortcutRow(key, description string) string {
	return "  " + keyStyle.Width(20).Render(key) + subtle.Render(description)
}

func providerLabel(provider core.Provider) string {
	color := "#94A3B8"
	switch provider {
	case core.Claude:
		color = "#E8956F"
	case core.Codex:
		color = "#34D399"
	case core.Gemini:
		color = "#60A5FA"
	case core.Agy:
		color = "#A78BFA"
	case core.Pi:
		color = "#FBBF24"
	case core.OpenCode:
		color = "#38BDF8"
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(color)).Render(string(provider))
}
