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
	"github.com/polera/tokenhawk/internal/pricing"
	"github.com/polera/tokenhawk/internal/sessionsearch"
	"github.com/polera/tokenhawk/internal/timerange"
)

type RefreshMsg struct{}
type sessionsMsg struct {
	sessions     []core.Session
	reported     []core.ReportedCost
	reportedDays []time.Time
	err          error
	background   bool
}
type exportMsg struct {
	path   string
	err    error
	detail bool
}

type Model struct {
	monitor                      *monitor.Monitor
	prices                       *pricing.Catalog
	table                        table.Model
	sessions, shown              []core.Session
	reportedCosts                []core.ReportedCost
	reportedCostDays             []time.Time
	width, height, tab, sortMode int
	provider                     core.Provider
	search                       string
	searching, detail            bool
	detailSession                *core.Session
	detailOffset                 int
	detailSearch                 string
	detailSearchDraft            string
	detailSearching              bool
	detailSearchMatch            int
	detailNotice                 string
	detailPrompts                sessionsearch.Report
	detailPromptsLoading         bool
	detailPromptsError           string
	detailPromptProvider         core.Provider
	detailPromptSessionID        string
	detailPromptRequest          int
	notice                       string
	layout                       int
	spendSpec                    string
	spendSince                   time.Time
	spendOffset                  int
	sinceInput                   bool
	sinceDraft                   string
	transcriptQuery              string
	transcriptDraft              string
	transcriptInput              bool
	transcriptLoading            bool
	transcriptReport             sessionsearch.Report
	transcriptOffset             int
	transcriptCursor             int
	transcriptRequest            int
	help                         bool
}

const (
	highInputAlarmTokens int64   = 100_000
	minimumCacheRatio    float64 = 0.80
	tableOuterWidth      int     = 2
	tableCellPadding     int     = 2
	defaultSpendSpec     string  = "7d"
)

func New(mon *monitor.Monitor, catalogs ...*pricing.Catalog) Model {
	t := table.New(table.WithFocused(true), table.WithHeight(15), table.WithStyles(tableTheme(true)))
	m := Model{monitor: mon, table: t}
	if len(catalogs) > 0 {
		m.prices = catalogs[0]
	}
	// The built-in default is a constant this package controls, so it parses.
	_ = m.setSpendSpec(defaultSpendSpec)
	return m
}

// WithSpendWindow opens Tokenhawk on the spend view for a caller-supplied
// window, so `tokenhawk --since 30d` lands where the flag is meaningful.
func (m Model) WithSpendWindow(spec string) (Model, error) {
	if err := m.setSpendSpec(spec); err != nil {
		return m, err
	}
	m.tab = spendTab
	return m, nil
}

// setSpendSpec resolves spec once so every rebuild filters against a stable
// instant instead of drifting with the clock between renders.
func (m *Model) setSpendSpec(spec string) error {
	since, err := timerange.Parse(spec, time.Now(), false)
	if err != nil {
		return err
	}
	m.spendSpec = spec
	m.spendSince = since
	m.spendOffset = 0
	return nil
}
func (m Model) Init() tea.Cmd { return tea.Batch(m.load(false), tea.RequestBackgroundColor) }
func (m Model) load(background bool) tea.Cmd {
	return func() tea.Msg {
		s, e := m.monitor.Sessions(context.Background(), core.Filter{})
		if e != nil {
			return sessionsMsg{err: e, background: background}
		}
		reported, days, e := m.monitor.ReportedCosts(context.Background())
		return sessionsMsg{sessions: s, reported: reported, reportedDays: days, err: e, background: background}
	}
}

// FilterRefresh prevents monitor-driven messages from reaching Bubble Tea
// while a historical session view is visible. Returning nil here matters: an
// ignored message in Update would still cause Bubble Tea to call View again.
func FilterRefresh(model tea.Model, msg tea.Msg) tea.Msg {
	var m Model
	switch current := model.(type) {
	case Model:
		m = current
	case *Model:
		m = *current
	default:
		return msg
	}
	switch message := msg.(type) {
	case RefreshMsg:
		if !m.backgroundRefreshEnabled() {
			return nil
		}
	case sessionsMsg:
		if message.background && !m.backgroundRefreshEnabled() {
			return nil
		}
	}
	return msg
}

func (m Model) backgroundRefreshEnabled() bool {
	if m.detail {
		session, ok := m.selectedSession()
		return ok && session.Active
	}
	// Inactive sessions and on-demand transcript results are historical views.
	return m.tab != 1 && m.tab != transcriptTab
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch x := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = x.Width
		m.height = x.Height
		m.resize()
		return m, nil
	case tea.BackgroundColorMsg:
		m.table.SetStyles(tableTheme(x.IsDark()))
		return m, nil
	case RefreshMsg:
		if !m.backgroundRefreshEnabled() {
			return m, nil
		}
		return m, m.load(true)
	case sessionsMsg:
		if x.background && !m.backgroundRefreshEnabled() {
			return m, nil
		}
		if x.err != nil {
			m.notice = x.err.Error()
		} else {
			m.sessions = x.sessions
			m.reportedCosts = x.reported
			m.reportedCostDays = x.reportedDays
			m.rebuild()
		}
		return m, nil
	case transcriptSearchMsg:
		m.applyTranscriptSearch(x)
		return m, nil
	case detailPromptsMsg:
		m.applyDetailPrompts(x)
		return m, nil
	case exportMsg:
		status := "exported " + x.path
		if x.err != nil {
			status = "export failed: " + x.err.Error()
		}
		if x.detail && m.detail {
			m.detailNotice = status
		} else {
			m.notice = status
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.sinceInput {
			return m.updateSinceInput(x)
		}
		if m.transcriptInput {
			return m.updateTranscriptInput(x)
		}
		if m.detailSearching {
			return m.updateDetailSearch(x)
		}
		if m.searching {
			return m.updateSearch(x)
		}
		if m.help {
			if x.String() == "ctrl+c" {
				return m, tea.Quit
			}
			m.help = false
			return m, nil
		}
		if x.String() == "?" {
			m.help = true
			return m, nil
		}
		if m.tab == transcriptTab && !m.detail {
			return m.updateTranscript(x)
		}
		if m.textTab() && !m.detail {
			return m.updateSpend(x)
		}
		if m.detail {
			if x.String() == "e" || x.String() == "x" {
				if selected, ok := m.selectedSession(); ok {
					format := "json"
					if x.String() == "x" {
						format = "csv"
					}
					m.detailNotice = "exporting " + strings.ToUpper(format) + "…"
					return m, m.exportDetail(format, selected)
				}
			}
			switch x.String() {
			case "/":
				m.detailSearching = true
				m.detailSearchDraft = m.detailSearch
			case "r":
				if selected, ok := m.selectedSession(); ok {
					return m, m.startDetailPrompts(selected)
				}
			case "n":
				m.moveDetailSearch(1)
			case "N":
				m.moveDetailSearch(-1)
			case "j", "down":
				m.scrollDetail(1)
			case "k", "up":
				m.scrollDetail(-1)
			case "pgdown", "ctrl+f", "space", "right", "l":
				m.scrollDetail(m.detailViewport())
			case "pgup", "ctrl+b", "left", "h":
				m.scrollDetail(-m.detailViewport())
			case "g", "home":
				m.setDetailScrollOffset(0)
			case "G", "end":
				m.setDetailScrollOffset(m.detailMaxOffset())
			}
			if x.String() == "esc" || x.String() == "enter" || x.String() == "q" {
				m.closeDetail()
			}
			return m, nil
		}
		switch x.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab", "right", "l", "]":
			m.navigateView(1)
		case "shift+tab", "left", "h", "[":
			m.navigateView(-1)
		case "1":
			m.tab = 0
			m.rebuild()
		case "2":
			m.tab = 1
			m.rebuild()
		case "3":
			m.tab = spendTab
			m.spendOffset = 0
			m.rebuild()
		case "4":
			m.openTranscriptSearch()
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
				m.detailSession = nil
				m.detail = true
				m.detailOffset = 0
				m.detailNotice = ""
				if selected, ok := m.selectedSession(); ok {
					return m, m.startDetailPrompts(selected)
				}
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

// updateSpend drives the spend view. It never falls through to the session
// table, whose cursor is meaningless here.
func (m Model) updateSpend(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab", "right", "l", "]":
		m.navigateView(1)
	case "shift+tab", "left", "h", "[":
		m.navigateView(-1)
	case "1", "2":
		m.tab = int(k.String()[0] - '1')
		m.spendOffset = 0
		m.rebuild()
	case "3":
		m.tab = spendTab
		m.spendOffset = 0
		m.rebuild()
	case "4":
		m.openTranscriptSearch()
	case "i":
		m.tab = 0
		m.spendOffset = 0
		m.rebuild()
	case "p":
		m.cycleProvider()
		m.rebuild()
	case "t":
		m.cycleSpendWindow()
	case "d":
		m.sinceInput = true
		m.sinceDraft = m.spendSpec
		m.notice = "since: RFC3339, YYYY-MM-DD, 7d, 3mo, today, mtd, ytd, or all; enter applies, esc cancels"
	case "/":
		m.searching = true
		m.notice = "type to filter projects/models; enter applies, esc cancels"
	case "e":
		return m, m.export("json")
	case "x":
		return m, m.export("csv")
	case "j", "down":
		m.scrollSpend(1)
	case "k", "up":
		m.scrollSpend(-1)
	case "pgdown", "ctrl+f":
		m.scrollSpend(max(1, m.height-4))
	case "pgup", "ctrl+b":
		m.scrollSpend(-max(1, m.height-4))
	case "g", "home":
		m.spendOffset = 0
	case "G", "end":
		m.spendOffset = m.spendMaxOffset()
	}
	return m, nil
}

func (m Model) updateSinceInput(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "enter":
		if err := m.setSpendSpec(strings.TrimSpace(m.sinceDraft)); err != nil {
			m.notice = "since: " + err.Error()
			return m, nil
		}
		m.sinceInput = false
		m.tab = spendTab
		m.notice = ""
		m.rebuild()
	case "esc":
		m.sinceInput = false
		m.sinceDraft = ""
		m.notice = ""
	case "backspace":
		r := []rune(m.sinceDraft)
		if len(r) > 0 {
			m.sinceDraft = string(r[:len(r)-1])
		}
	default:
		if k.Key().Text != "" {
			m.sinceDraft += k.Key().Text
		}
	}
	return m, nil
}

func (m *Model) cycleSpendWindow() {
	_ = m.setSpendSpec(timerange.Next(m.spendSpec))
	m.rebuild()
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
		m.provider = core.Agy
	case core.Agy:
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
	m.table.SetWidth(w - tableOuterWidth)
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
	case w < 96:
		m.layout = 0
		session := flexibleColumnWidth(w, 38, 5, 8)
		m.table.SetColumns([]table.Column{{Title: "Provider", Width: 8}, {Title: "Session", Width: session}, {Title: "Agents", Width: 6}, {Title: "I/O · Ratio", Width: 16}, {Title: "Total", Width: 8}})
	case w < 131:
		m.layout = 1
		session := flexibleColumnWidth(w, 66, 10, 8)
		m.table.SetColumns([]table.Column{{Title: "Provider", Width: 8}, {Title: "Session", Width: session}, {Title: "Agents", Width: 6}, {Title: "Input", Width: 7}, {Title: "Cached", Width: 7}, {Title: "Output", Width: 7}, {Title: "I:O", Width: 7}, {Title: "Total", Width: 8}, {Title: "Cost USD", Width: 9}, {Title: "Updated", Width: 7}})
	default:
		m.layout = 2
		session := flexibleColumnWidth(w, 95, 12, 10)
		m.table.SetColumns([]table.Column{{Title: "Provider", Width: 8}, {Title: "Session", Width: session}, {Title: "Agents", Width: 6}, {Title: "Model", Width: 16}, {Title: "Input", Width: 8}, {Title: "Cached", Width: 8}, {Title: "Output", Width: 8}, {Title: "I:O", Width: 7}, {Title: "Reason", Width: 8}, {Title: "Total", Width: 9}, {Title: "Cost USD", Width: 10}, {Title: "Updated", Width: 7}})
		return
	}
}
func flexibleColumnWidth(terminalWidth, fixedWidth, columnCount, minimum int) int {
	available := terminalWidth - tableOuterWidth - fixedWidth - columnCount*tableCellPadding
	return max(minimum, available)
}
func (m *Model) rebuild() {
	m.refreshSpendWindow()
	q := strings.ToLower(m.search)
	m.shown = nil
	for _, s := range m.sessions {
		if m.provider != "" && s.Provider != m.provider {
			continue
		}
		if m.tab == spendTab {
			if !m.spendSince.IsZero() && s.UpdatedAt.Before(m.spendSince) {
				continue
			}
		} else if m.tab == 0 && !s.Active || m.tab == 1 && s.Active {
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
		provider := providerLabel(s.Provider)
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
			rows = append(rows, table.Row{provider, label, agents, breakdown, total})
		case 1:
			rows = append(rows, table.Row{provider, label, agents, input, cached, output, io, total, cost, relative(s.UpdatedAt)})
		default:
			rows = append(rows, table.Row{provider, label, agents, modelNames(s), input, cached, output, io, human(u.Reasoning), total, cost, relative(s.UpdatedAt)})
		}
	}
	m.table.SetRows(rows)
	if len(rows) > 0 && m.table.Cursor() < 0 {
		m.table.SetCursor(0)
	}
}

// refreshSpendWindow re-resolves a relative spec on every rebuild so a window
// such as "last 24 hours" keeps rolling while Tokenhawk stays open. A spec that
// stops parsing keeps the last resolved bound rather than silently widening.
func (m *Model) refreshSpendWindow() {
	if since, err := timerange.Parse(m.spendSpec, time.Now(), false); err == nil {
		m.spendSince = since
	}
}

func (m *Model) scrollSpend(delta int) {
	m.spendOffset = min(max(0, m.spendOffset+delta), m.spendMaxOffset())
}

func (m Model) spendMaxOffset() int {
	return max(0, len(strings.Split(m.spendContent(), "\n"))-max(1, m.spendViewport()))
}

// spendViewport is the line budget left for the spend body once the dashboard
// header, prompt, and footer have claimed their rows.
func (m Model) spendViewport() int {
	return max(3, m.height-m.chromeHeight())
}

func (m Model) export(format string) tea.Cmd {
	return m.exportSessions(format, append([]core.Session(nil), m.shown...))
}
func (m Model) exportSessions(format string, sessions []core.Session) tea.Cmd {
	return func() tea.Msg {
		name := fmt.Sprintf("tokenhawk-%s.%s", time.Now().Format("20060102-150405"), format)
		path, _ := filepath.Abs(name)
		err := exporter.Write(path, format, sessions)
		return exportMsg{path: path, err: err}
	}
}

func (m Model) exportDetail(format string, session core.Session) tea.Cmd {
	conversation, loaded := m.detailExportConversation(session)
	mon := m.monitor
	return func() tea.Msg {
		name := fmt.Sprintf("tokenhawk-%s.%s", time.Now().Format("20060102-150405"), format)
		path, _ := filepath.Abs(name)
		if !loaded {
			if mon == nil {
				return exportMsg{path: path, err: fmt.Errorf("conversation is unavailable"), detail: true}
			}
			report, err := mon.Conversation(context.Background(), session.Provider, session.ID)
			if err != nil {
				return exportMsg{path: path, err: fmt.Errorf("load conversation: %w", err), detail: true}
			}
			if len(report.Unsupported) > 0 {
				return exportMsg{path: path, err: fmt.Errorf("conversation is unavailable for %s", report.Unsupported[0]), detail: true}
			}
			conversation = exportConversation(report.Matches)
		}
		err := exporter.WriteDetail(path, format, session, conversation)
		return exportMsg{path: path, err: err, detail: true}
	}
}

func (m Model) detailExportConversation(session core.Session) ([]exporter.Message, bool) {
	loaded := m.detailPromptProvider == session.Provider && m.detailPromptSessionID == session.ID &&
		!m.detailPromptsLoading && m.detailPromptsError == "" && len(m.detailPrompts.Unsupported) == 0
	if !loaded {
		return nil, false
	}
	return exportConversation(m.detailPrompts.Matches), true
}

func exportConversation(messages []sessionsearch.Match) []exporter.Message {
	var conversation []exporter.Message
	for _, message := range messages {
		conversation = append(conversation, exporter.Message{
			Timestamp: message.Timestamp, SubagentID: message.SubagentID, Role: message.Role, Text: message.Snippet,
		})
	}
	return conversation
}

func (m Model) View() tea.View {
	var body string
	if m.help {
		body = m.helpView()
	} else if m.detail {
		body = m.detailView()
	} else {
		body = m.dashboard()
	}
	v := tea.NewView(body)
	v.AltScreen = true
	v.WindowTitle = "Tokenhawk - token usage monitor"
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
func (m Model) dashboard() string {
	header, footer := m.chrome()
	body := m.table.View()
	if m.tab == transcriptTab {
		body = m.transcriptBody()
	} else if m.textTab() {
		body = m.spendBody()
	}
	return header + "\n\n" + body + "\n" + footer
}

// chrome renders the parts that frame every dashboard body, so the spend view
// and the session table agree on how many rows are left for content.
func (m Model) chrome() (string, string) {
	return m.dashboardChrome()
}

func (m Model) chromeHeight() int {
	header, footer := m.chrome()
	return lipgloss.Height(header) + lipgloss.Height(footer) + 1
}

// textTab reports whether the active tab renders a scrolling text report
// rather than the session table.
func (m Model) textTab() bool {
	return m.tab == spendTab || m.tab == transcriptTab
}

// spendBody renders the active text report clipped to the available rows, with
// the same scroll affordance the session detail uses.
func (m Model) spendBody() string {
	content := m.spendContent()
	lines := strings.Split(content, "\n")
	visible := m.spendViewport()
	if m.height <= 0 || len(lines) <= visible {
		return content
	}
	offset := min(max(0, m.spendOffset), max(0, len(lines)-visible))
	end := min(len(lines), offset+visible-1)
	return strings.Join(lines[offset:end], "\n") + "\n" + muted.Render(fmt.Sprintf("↑/↓ scroll  %d–%d/%d", offset+1, end, len(lines)))
}
func (m Model) detailView() string {
	content := m.detailContent()
	keys := "↑/↓ line  PgUp/PgDn page  / find  r reload  e JSON  x CSV  Enter/Esc back"
	if m.detailSearching {
		keys = "/ " + m.detailSearchDraft + titleStyle.Render("▌") + "  Enter find  Esc cancel"
	} else if m.detailSearch != "" {
		current, total := m.detailSearchPosition()
		if total == 0 {
			keys = fmt.Sprintf("/ %s  ·  no matches  ·  / edit  r reload  Enter/Esc back", m.detailSearch)
		} else {
			keys = fmt.Sprintf("/ %s  ·  match %d/%d  ·  n/N next/prev  / edit  Enter/Esc back", m.detailSearch, current, total)
		}
	}
	footer := m.detailFooter(keys)
	lines := strings.Split(content, "\n")
	visible := m.detailViewport()
	if m.height <= 0 || len(lines) <= visible {
		return content + "\n" + footer
	}
	offset := min(max(0, m.detailScrollOffset()), m.detailMaxOffset())
	end := min(len(lines), offset+visible)
	page := offset/visible + 1
	pages := max(1, (len(lines)+visible-1)/visible)
	if offset == m.detailMaxOffset() {
		page = pages
	}
	pageStatus := fmt.Sprintf("page %d/%d  •  lines %d–%d/%d  •  %s", page, pages, offset+1, end, len(lines), keys)
	footer = m.detailFooter(pageStatus)
	return strings.Join(lines[offset:end], "\n") + "\n" + footer
}

func (m Model) detailFooter(keys string) string {
	footer := muted.Render(keys)
	if m.detailNotice == "" {
		return footer
	}
	statusStyle := good
	if strings.HasPrefix(m.detailNotice, "export failed:") {
		statusStyle = alarmStyle
	}
	return footer + "\n" + statusStyle.Render(m.detailNoticeText())
}

func (m Model) detailNoticeText() string {
	width := 80
	if m.width > 0 {
		width = max(1, m.width)
	}
	return wrapPromptText(m.detailNotice, width)
}

func (m Model) detailContent() string {
	s, ok := m.selectedSession()
	if !ok {
		return "No session selected"
	}
	var b strings.Builder
	b.WriteString(muted.Render(strings.ToUpper(string(s.Provider))+"  /  SESSION DETAIL") + "\n")
	status := statusStyle(s.Active).Render(map[bool]string{true: "● ACTIVE", false: "○ HISTORY"}[s.Active])
	b.WriteString(titleStyle.Render("SESSION "+s.ID) + "  " + status + "\n\n")
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
	b.WriteString(m.detailPromptContent(s))
	return b.String()
}
func (m *Model) scrollDetail(delta int) {
	m.setDetailScrollOffset(min(max(0, m.detailScrollOffset()+delta), m.detailMaxOffset()))
}

func (m Model) detailScrollOffset() int {
	return m.detailOffset
}

func (m *Model) setDetailScrollOffset(offset int) {
	m.detailOffset = offset
}
func (m Model) detailMaxOffset() int {
	lines := len(strings.Split(m.detailContent(), "\n"))
	visible := m.detailViewport()
	if lines <= visible {
		return 0
	}
	// Bottom-align the final viewport. Rounding this to a page boundary leaves a
	// short last page (and its footer) stranded in the middle of the terminal.
	return lines - visible
}

func (m Model) detailViewport() int {
	statusRows := 0
	if m.detailNotice != "" {
		statusRows = len(strings.Split(m.detailNoticeText(), "\n"))
	}
	return max(1, m.height-2-statusRows)
}
func (m Model) tableCursorSession() int {
	return m.table.Cursor()
}

func (m Model) selectedSession() (core.Session, bool) {
	if m.detailSession != nil {
		for _, session := range m.sessions {
			if session.Provider == m.detailSession.Provider && session.ID == m.detailSession.ID {
				return session, true
			}
		}
		return *m.detailSession, true
	}
	idx := m.tableCursorSession()
	if idx < 0 || idx >= len(m.shown) {
		return core.Session{}, false
	}
	return m.shown[idx], true
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
		return "-"
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
	case core.Agy:
		command = "agy --conversation " + shellQuote(s.ID)
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
		return brandStyle.Render("TOKENHAWK")
	}
	return brandStyle.Render("TOKENHAWK") + "\n" + muted.Render("session token monitor")
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
		return "-"
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
		return fmt.Sprintf("$%.6f API rate", u.CostUSD)
	case "partially priced":
		return fmt.Sprintf("$%.6f+ API rate (partially priced)", u.CostUSD)
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
