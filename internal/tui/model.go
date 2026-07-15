package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/polera/tokenhawk/internal/core"
	exporter "github.com/polera/tokenhawk/internal/export"
	"github.com/polera/tokenhawk/internal/monitor"
)

type RefreshMsg struct{}
type sessionsMsg struct {
	sessions []core.Session
	err      error
}
type exportMsg struct {
	path string
	err  error
}

type Model struct {
	monitor                      *monitor.Monitor
	table                        table.Model
	sessions, shown              []core.Session
	width, height, tab, sortMode int
	provider                     core.Provider
	search                       string
	searching, detail            bool
	detailOffset                 int
	notice                       string
	layout                       int
}

var titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7cc8"))
var hawkStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff7cc8"))
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
var good = lipgloss.NewStyle().Foreground(lipgloss.Color("#65d46e"))
var alarmStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ff5f56"))

const (
	highInputAlarmTokens int64 = 100_000
	minimumCacheRatio          = 0.80
)

func New(mon *monitor.Monitor) Model {
	t := table.New(table.WithFocused(true), table.WithHeight(15))
	return Model{monitor: mon, table: t}
}
func (m Model) Init() tea.Cmd { return tea.Batch(m.load(), tea.RequestBackgroundColor) }
func (m Model) load() tea.Cmd {
	return func() tea.Msg {
		s, e := m.monitor.Sessions(context.Background(), core.Filter{})
		return sessionsMsg{s, e}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch x := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = x.Width
		m.height = x.Height
		m.resize()
		return m, nil
	case tea.BackgroundColorMsg:
		styles := table.DefaultStyles()
		if x.IsDark() {
			styles.Selected = styles.Selected.Foreground(lipgloss.Color("#ff7cc8"))
		} else {
			styles.Selected = styles.Selected.Foreground(lipgloss.Color("#8f1d65"))
		}
		m.table.SetStyles(styles)
		return m, nil
	case RefreshMsg:
		return m, m.load()
	case sessionsMsg:
		if x.err != nil {
			m.notice = x.err.Error()
		} else {
			m.sessions = x.sessions
			m.rebuild()
		}
		return m, nil
	case exportMsg:
		if x.err != nil {
			m.notice = "export failed: " + x.err.Error()
		} else {
			m.notice = "exported " + x.path
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.searching {
			return m.updateSearch(x)
		}
		if m.detail {
			if x.String() == "e" || x.String() == "x" {
				idx := m.tableCursorSession()
				if idx >= 0 && idx < len(m.shown) {
					format := "json"
					if x.String() == "x" {
						format = "csv"
					}
					return m, m.exportSessions(format, []core.Session{m.shown[idx]})
				}
			}
			switch x.String() {
			case "j", "down":
				m.scrollDetail(1)
			case "k", "up":
				m.scrollDetail(-1)
			case "pgdown", "ctrl+f":
				m.scrollDetail(max(1, m.height-4))
			case "pgup", "ctrl+b":
				m.scrollDetail(-max(1, m.height-4))
			case "g", "home":
				m.detailOffset = 0
			case "G", "end":
				m.detailOffset = m.detailMaxOffset()
			}
			if x.String() == "esc" || x.String() == "enter" || x.String() == "q" {
				m.detail = false
				m.detailOffset = 0
			}
			return m, nil
		}
		switch x.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "1":
			m.tab = 0
			m.rebuild()
		case "2":
			m.tab = 1
			m.rebuild()
		case "3":
			m.tab = 2
			m.rebuild()
		case "i":
			m.toggleActiveInactive()
		case "p":
			m.cycleProvider()
			m.rebuild()
		case "s":
			m.sortMode = (m.sortMode + 1) % 3
			m.rebuild()
		case "/":
			m.searching = true
			m.notice = "type to filter projects/models; enter applies, esc cancels"
		case "enter":
			if len(m.table.SelectedRow()) > 0 {
				m.detail = true
				m.detailOffset = 0
			}
		case "e":
			return m, m.export("json")
		case "x":
			return m, m.export("csv")
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m Model) updateSearch(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "enter":
		m.searching = false
		m.notice = ""
		m.rebuild()
	case "esc":
		m.searching = false
		m.search = ""
		m.notice = ""
		m.rebuild()
	case "backspace":
		r := []rune(m.search)
		if len(r) > 0 {
			m.search = string(r[:len(r)-1])
		}
		m.rebuild()
	default:
		if k.Key().Text != "" {
			m.search += k.Key().Text
			m.rebuild()
		}
	}
	return m, nil
}
func (m *Model) cycleProvider() {
	switch m.provider {
	case "":
		m.provider = core.Claude
	case core.Claude:
		m.provider = core.Codex
	case core.Codex:
		m.provider = core.Gemini
	case core.Gemini:
		m.provider = core.Pi
	case core.Pi:
		m.provider = core.OpenCode
	default:
		m.provider = ""
	}
}
func (m *Model) toggleActiveInactive() {
	if m.tab == 0 {
		m.tab = 1
	} else {
		m.tab = 0
	}
	m.rebuild()
	if len(m.shown) > 0 {
		m.table.SetCursor(0)
	}
}
func (m *Model) resize() {
	cursor := m.table.Cursor()
	// Bubbles renders immediately in SetColumns. Clear old-shape rows first so
	// crossing a responsive breakpoint cannot pair 8-cell rows with 10 columns.
	m.table.SetRows(nil)
	w := max(30, m.width)
	m.table.SetWidth(w - 2)
	headerHeight := 9
	if w >= 60 {
		headerHeight = 14
	}
	m.table.SetHeight(max(5, m.height-headerHeight))
	m.setColumns()
	m.rebuild()
	if len(m.table.Rows()) > 0 {
		m.table.SetCursor(cursor)
	}
}
func (m *Model) setColumns() {
	w := max(30, m.width)
	switch {
	case w < 80:
		m.layout = 0
		session := max(8, w-47)
		m.table.SetColumns([]table.Column{{Title: "Provider", Width: 8}, {Title: "Session", Width: session}, {Title: "Agents", Width: 6}, {Title: "I/O · Ratio", Width: 16}, {Title: "Total", Width: 8}})
	case w < 120:
		m.layout = 1
		session := max(8, w-84)
		m.table.SetColumns([]table.Column{{Title: "Provider", Width: 8}, {Title: "Session", Width: session}, {Title: "Agents", Width: 6}, {Title: "Input", Width: 7}, {Title: "Cached", Width: 7}, {Title: "Output", Width: 7}, {Title: "I:O", Width: 7}, {Title: "Total", Width: 8}, {Title: "Cost USD", Width: 9}, {Title: "Updated", Width: 7}})
	default:
		m.layout = 2
		session := max(10, w-115)
		m.table.SetColumns([]table.Column{{Title: "Provider", Width: 8}, {Title: "Session", Width: session}, {Title: "Agents", Width: 6}, {Title: "Model", Width: 16}, {Title: "Input", Width: 8}, {Title: "Cached", Width: 8}, {Title: "Output", Width: 8}, {Title: "I:O", Width: 7}, {Title: "Reason", Width: 8}, {Title: "Total", Width: 9}, {Title: "Cost USD", Width: 10}, {Title: "Updated", Width: 7}})
		return
	}
}
func (m *Model) rebuild() {
	q := strings.ToLower(m.search)
	m.shown = nil
	for _, s := range m.sessions {
		if m.provider != "" && s.Provider != m.provider {
			continue
		}
		if m.tab == 0 && !s.Active || m.tab == 1 && s.Active {
			continue
		}
		models := modelNames(s)
		if q != "" && !strings.Contains(strings.ToLower(s.Project+" "+models+" "+subagentSearchText(s)), q) {
			continue
		}
		m.shown = append(m.shown, s)
	}
	switch m.sortMode {
	case 1:
		sort.SliceStable(m.shown, func(i, j int) bool { return m.shown[i].Totals().Total > m.shown[j].Totals().Total })
	case 2:
		sort.SliceStable(m.shown, func(i, j int) bool { return m.shown[i].Totals().CostUSD > m.shown[j].Totals().CostUSD })
	default:
		sort.SliceStable(m.shown, func(i, j int) bool { return m.shown[i].UpdatedAt.After(m.shown[j].UpdatedAt) })
	}
	rows := make([]table.Row, 0, len(m.shown))
	for _, s := range m.shown {
		u := s.Totals()
		label, agents := sessionLabel(s), agentCount(s)
		input, cached, output := human(u.Input), human(u.CachedInput), human(u.Output)
		io, total, cost := ratioText(u.Input, u.Output), human(u.Total), costText(u)
		if cacheAlarm(u) {
			label = alarmStyle.Render("⚠ " + label)
			input = alarmStyle.Render(input)
			cached = alarmStyle.Render(cached)
			io = alarmStyle.Render(io)
			total = alarmStyle.Render(total)
			cost = alarmStyle.Render(cost)
		}
		switch m.layout {
		case 0:
			breakdown := input + "/" + output + " " + io
			if cacheAlarm(u) {
				breakdown = alarmStyle.Render(human(u.Input) + "/" + human(u.Output) + " " + ratioText(u.Input, u.Output))
			}
			rows = append(rows, table.Row{string(s.Provider), label, agents, breakdown, total})
		case 1:
			rows = append(rows, table.Row{string(s.Provider), label, agents, input, cached, output, io, total, cost, relative(s.UpdatedAt)})
		default:
			rows = append(rows, table.Row{string(s.Provider), label, agents, modelNames(s), input, cached, output, io, human(u.Reasoning), total, cost, relative(s.UpdatedAt)})
		}
	}
	m.table.SetRows(rows)
	if len(rows) > 0 && m.table.Cursor() < 0 {
		m.table.SetCursor(0)
	}
}
func (m Model) export(format string) tea.Cmd {
	return m.exportSessions(format, append([]core.Session(nil), m.shown...))
}
func (m Model) exportSessions(format string, sessions []core.Session) tea.Cmd {
	return func() tea.Msg {
		name := fmt.Sprintf("tokenhawk-%s.%s", time.Now().Format("20060102-150405"), format)
		path, _ := filepath.Abs(name)
		err := exporter.Write(path, format, sessions)
		return exportMsg{path, err}
	}
}

func (m Model) View() tea.View {
	var body string
	if m.detail {
		body = m.detailView()
	} else {
		body = m.dashboard()
	}
	v := tea.NewView(body)
	v.AltScreen = true
	v.WindowTitle = "Tokenhawk — token usage monitor"
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
func (m Model) dashboard() string {
	active := 0
	runningAgents := 0
	for _, s := range m.sessions {
		if s.Active {
			active++
		}
		runningAgents += s.RunningSubagents()
	}
	cacheAlarms := activeCacheAlarms(m.sessions)
	tabs := []string{"1 Active", "2 Inactive Sessions", "3 All Sessions"}
	tabs[m.tab] = titleStyle.Render(tabs[m.tab])
	provider := "all"
	if m.provider != "" {
		provider = string(m.provider)
	}
	sortName := []string{"updated", "tokens", "cost"}[m.sortMode]
	header := hawkBrand(m.width) + "\n" + strings.Join(tabs, "  ") + "\n" + fmt.Sprintf("%s active  •  %d inactive  •  %s subagents running  •  showing %d of %d sessions\nprovider: %s  •  sort: %s", good.Render(fmt.Sprint(active)), len(m.sessions)-active, good.Render(fmt.Sprint(runningAgents)), len(m.shown), len(m.sessions), provider, sortName)
	if cacheAlarms > 0 {
		header += "\n" + alarmStyle.Render(fmt.Sprintf("⚠ %d high-input session(s) below 80%% cache ratio", cacheAlarms))
	}
	search := ""
	if m.searching || m.search != "" {
		search = "\n/ " + m.search + "▌"
	}
	status := m.monitor.Status()
	footer := fmt.Sprintf("i active/inactive  p provider  s sort  / filter  enter details  e JSON  x CSV  q quit  •  indexed %d files", status.Files)
	if status.Scanning {
		footer += " • scanning…"
	}
	if status.Warning != "" {
		footer += " • warning: " + status.Warning
	}
	if m.notice != "" {
		footer += "\n" + m.notice
	}
	return header + search + "\n\n" + m.table.View() + "\n" + muted.Render(footer)
}
func (m Model) detailView() string {
	content := m.detailContent()
	lines := strings.Split(content, "\n")
	visible := max(1, m.height-2)
	if m.height <= 0 || len(lines) <= visible {
		return content + "\n" + muted.Render("e JSON  x CSV  Enter/Esc back")
	}
	offset := min(max(0, m.detailOffset), max(0, len(lines)-visible))
	end := min(len(lines), offset+visible)
	footer := fmt.Sprintf("↑/↓ scroll  %d–%d/%d  •  e JSON  x CSV  Enter/Esc back", offset+1, end, len(lines))
	return strings.Join(lines[offset:end], "\n") + "\n" + muted.Render(footer)
}

func (m Model) detailContent() string {
	idx := m.tableCursorSession()
	if idx < 0 || idx >= len(m.shown) {
		return "No session selected"
	}
	s := m.shown[idx]
	var b strings.Builder
	b.WriteString(titleStyle.Render("SESSION "+s.ID) + "\n\n")
	fmt.Fprintf(&b, "Provider: %s\nProject: %s\nStarted: %s\nUpdated: %s (%s)\nStatus: %s\nSource: %s\nResume: %s\n\n", s.Provider, s.Project, s.StartedAt.Format(time.RFC3339), s.UpdatedAt.Format(time.RFC3339), relative(s.UpdatedAt), map[bool]string{true: "active", false: "inactive"}[s.Active], s.SourceHealth, resumeCommand(s))
	total := s.Totals()
	direct := s.DirectTotals()
	fmt.Fprintf(&b, "%s  tokens %s  %s\n\n", titleStyle.Render("SESSION TOTAL"), human(total.Total), costDetail(total))
	if cacheAlarm(total) {
		fmt.Fprintf(&b, "%s\n\n", cacheAlarmText("session total", total))
	}
	fmt.Fprintf(&b, "%s  tokens %s  %s\n\n", titleStyle.Render("PARENT USAGE"), human(direct.Total), costDetail(direct))
	if cacheAlarm(direct) {
		fmt.Fprintf(&b, "%s\n\n", cacheAlarmText("parent", direct))
	}
	for _, u := range s.Usage {
		fmt.Fprintf(&b, "%s\n  input %s  cached %s  cache write %s\n  output %s  input:output %s  reasoning %s  tool %s  total %s\n  %s\n\n", u.Model, human(u.Input), human(u.CachedInput), human(u.CacheCreation), human(u.Output), ratioText(u.Input, u.Output), human(u.Reasoning), human(u.Tool), human(u.Total), costDetail(u))
	}
	fmt.Fprintf(&b, "%s  %d running / %d total\n\n", titleStyle.Render("SUBAGENTS"), s.RunningSubagents(), len(s.Subagents))
	if len(s.Subagents) == 0 {
		b.WriteString(muted.Render("No subagents recorded for this session.") + "\n\n")
	}
	for _, a := range s.Subagents {
		name := a.Name
		if name == "" {
			name = shortID(a.ID)
		}
		status := muted.Render(a.Status)
		if a.Running {
			status = good.Render("● running")
		}
		fmt.Fprintf(&b, "%s  %s\n", titleStyle.Render(name), status)
		fmt.Fprintf(&b, "  id %s", a.ID)
		if a.AgentPath != "" {
			fmt.Fprintf(&b, "  path %s", a.AgentPath)
		}
		fmt.Fprintf(&b, "  updated %s (%s)\n", a.UpdatedAt.Format(time.RFC3339), relative(a.UpdatedAt))
		if cacheAlarm(a.Totals()) {
			fmt.Fprintf(&b, "  %s\n", cacheAlarmText("subagent", a.Totals()))
		}
		for _, u := range a.Usage {
			fmt.Fprintf(&b, "  %s\n    input %s  cached %s  cache write %s\n    output %s  input:output %s  reasoning %s  tool %s  total %s\n    %s\n", u.Model, human(u.Input), human(u.CachedInput), human(u.CacheCreation), human(u.Output), ratioText(u.Input, u.Output), human(u.Reasoning), human(u.Tool), human(u.Total), costDetail(u))
		}
		b.WriteString("\n")
	}
	return b.String()
}
func (m *Model) scrollDetail(delta int) {
	m.detailOffset = min(max(0, m.detailOffset+delta), m.detailMaxOffset())
}
func (m Model) detailMaxOffset() int {
	return max(0, len(strings.Split(m.detailContent(), "\n"))-max(1, m.height-2))
}
func (m Model) tableCursorSession() int {
	return m.table.Cursor()
}
func modelNames(s core.Session) string {
	seen := map[string]bool{}
	var n []string
	for _, u := range s.Usage {
		if !seen[u.Model] {
			n = append(n, u.Model)
			seen[u.Model] = true
		}
	}
	for _, a := range s.Subagents {
		for _, u := range a.Usage {
			if !seen[u.Model] {
				n = append(n, u.Model)
				seen[u.Model] = true
			}
		}
	}
	return strings.Join(n, ",")
}
func subagentSearchText(s core.Session) string {
	var values []string
	for _, a := range s.Subagents {
		values = append(values, a.ID, a.Name, a.AgentPath)
		for _, u := range a.Usage {
			values = append(values, u.Model)
		}
	}
	return strings.Join(values, " ")
}
func agentCount(s core.Session) string {
	if len(s.Subagents) == 0 {
		return "—"
	}
	return fmt.Sprintf("%d/%d", s.RunningSubagents(), len(s.Subagents))
}
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
func resumeCommand(s core.Session) string {
	var command string
	switch s.Provider {
	case core.Claude:
		command = "claude --resume " + shellQuote(s.ID)
	case core.Codex:
		command = "codex resume " + shellQuote(s.ID)
	case core.Gemini:
		command = "gemini --resume " + shellQuote(s.ID)
	case core.Pi:
		command = "pi --session " + shellQuote(s.ID)
	case core.OpenCode:
		command = "opencode --session " + shellQuote(s.ID)
	default:
		return "unavailable"
	}
	if s.Project != "" {
		return "cd " + shellQuote(s.Project) + " && " + command
	}
	return command
}
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
func sessionLabel(s core.Session) string {
	id := s.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return shortProject(s.Project) + "/" + id
}
func hawkBrand(width int) string {
	if width < 60 {
		return hawkStyle.Render("▒▓▓▓▓▒") + "  " + titleStyle.Render("TOKENHAWK")
	}
	hawk := eagleMark()
	wordmark := titleStyle.Render("TOKENHAWK") + "\n" + muted.Render("session token monitor")
	return lipgloss.JoinHorizontal(lipgloss.Center, hawk, "   ", wordmark)
}
func eagleMark() string {
	// 24x6 raster generated directly from the supplied angular hawk-head logo.
	lines := strings.Split(` ⢠⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣤⣄⡀
 ⢸⣿⣿⣿⣿⣿⢟⣫⣽⡿⣿⣿⣿⣿⣿⣿⣶⣶⣦⣤⣀
 ⢨⣭⣭⣭⣍⡑⢿⣿⣧⣀⣻⣿⣿⣟⣿⠋  ⠉⠙⢿⣷
 ⢸⣿⣿⣿⣿⣿⢆⣿⡿⣫⣽⣿⣿⣿⣶⣤⣤⣀⡀ ⣸⡿
 ⠘⠛⠛⠛⠛⠛⠈⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠛⠻⣷⡿⠁
                    ⠋`, "\n")
	for i, line := range lines {
		lines[i] = hawkStyle.Render(line)
	}
	return strings.Join(lines, "\n")
}
func shortProject(p string) string {
	if p == "" {
		return "unknown"
	}
	return filepath.Base(p)
}
func human(v int64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(v)/1e6)
	case v >= 1_000:
		return fmt.Sprintf("%.1fk", float64(v)/1e3)
	default:
		return fmt.Sprint(v)
	}
}
func ratioText(input, output int64) string {
	switch {
	case input == 0 && output == 0:
		return "—"
	case output == 0:
		return "∞:1"
	case input == 0:
		return "0:1"
	case input >= output:
		return compactDecimal(float64(input)/float64(output)) + ":1"
	default:
		return "1:" + compactDecimal(float64(output)/float64(input))
	}
}
func compactDecimal(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("%.0f", v)
	}
	if v >= 10 {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.2f", v)
}
func costText(u core.Usage) string {
	if u.PricingStatus == "priced" || u.PricingStatus == "reported" {
		return fmt.Sprintf("$%.4f", u.CostUSD)
	}
	if u.CostUSD > 0 {
		return fmt.Sprintf("$%.4f+", u.CostUSD)
	}
	return "unpriced"
}
func costDetail(u core.Usage) string {
	switch u.PricingStatus {
	case "reported":
		return fmt.Sprintf("$%.6f reported", u.CostUSD)
	case "priced":
		return fmt.Sprintf("$%.6f estimated (priced)", u.CostUSD)
	case "partially priced":
		return fmt.Sprintf("$%.6f+ estimated (partially priced)", u.CostUSD)
	default:
		return "unpriced"
	}
}
func cacheAlarm(u core.Usage) bool {
	return u.Input >= highInputAlarmTokens && float64(u.CachedInput)/float64(u.Input) < minimumCacheRatio
}
func activeCacheAlarms(sessions []core.Session) int {
	count := 0
	for _, s := range sessions {
		if s.Active && cacheAlarm(s.Totals()) {
			count++
		}
	}
	return count
}
func cacheAlarmText(scope string, u core.Usage) string {
	ratio := float64(u.CachedInput) / float64(u.Input) * 100
	return alarmStyle.Render(fmt.Sprintf("⚠ LOW CACHE: %s has %s input at %.1f%% cached (minimum 80%%)", scope, human(u.Input), ratio))
}
func relative(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
