package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/polera/tokenhawk/internal/sessionsearch"
)

const transcriptTab = 3
const transcriptResultLimit = 200

type transcriptSearchMsg struct {
	request int
	report  sessionsearch.Report
	err     error
}

func (m *Model) openTranscriptSearch() {
	m.tab = transcriptTab
	m.transcriptOffset = 0
	if m.transcriptQuery == "" {
		m.transcriptInput = true
		m.transcriptDraft = ""
		m.notice = "search user/assistant transcript text; enter runs, esc cancels"
	}
	m.rebuild()
}

func (m Model) updateTranscriptInput(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "enter":
		query := strings.TrimSpace(m.transcriptDraft)
		if query == "" {
			m.notice = "transcript search query cannot be empty"
			return m, nil
		}
		m.transcriptQuery = query
		m.transcriptInput = false
		m.notice = ""
		return m, m.startTranscriptSearch()
	case "esc":
		m.transcriptInput = false
		m.transcriptDraft = ""
		m.notice = ""
	case "backspace":
		runes := []rune(m.transcriptDraft)
		if len(runes) > 0 {
			m.transcriptDraft = string(runes[:len(runes)-1])
		}
	default:
		if k.Key().Text != "" {
			m.transcriptDraft += k.Key().Text
		}
	}
	return m, nil
}

func (m Model) updateTranscript(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab", "right", "l", "]":
		m.navigateView(1)
	case "shift+tab", "left", "h", "[":
		m.navigateView(-1)
	case "1", "2":
		m.tab = int(k.String()[0] - '1')
		m.rebuild()
	case "3":
		m.tab = spendTab
		m.spendOffset = 0
		m.rebuild()
	case "4", "/":
		m.transcriptInput = true
		m.transcriptDraft = m.transcriptQuery
		m.notice = "search user/assistant transcript text; enter runs, esc cancels"
	case "r":
		if m.transcriptQuery != "" {
			return m, m.startTranscriptSearch()
		}
	case "p":
		m.cycleProvider()
		m.rebuild()
		if m.transcriptQuery != "" {
			return m, m.startTranscriptSearch()
		}
	case "enter":
		return m, m.openTranscriptDetail()
	case "j", "down":
		m.moveTranscriptCursor(1)
	case "k", "up":
		m.moveTranscriptCursor(-1)
	case "pgdown", "ctrl+f":
		m.moveTranscriptCursor(max(1, m.height/4))
	case "pgup", "ctrl+b":
		m.moveTranscriptCursor(-max(1, m.height/4))
	case "g", "home":
		m.transcriptCursor = 0
		m.ensureTranscriptSelectionVisible()
	case "G", "end":
		m.transcriptCursor = max(0, len(m.transcriptReport.Matches)-1)
		m.ensureTranscriptSelectionVisible()
	}
	return m, nil
}

func (m *Model) startTranscriptSearch() tea.Cmd {
	m.transcriptRequest++
	request := m.transcriptRequest
	query := sessionsearch.Query{Text: m.transcriptQuery, Provider: m.provider, Limit: transcriptResultLimit}
	mon := m.monitor
	m.transcriptLoading = true
	m.transcriptOffset = 0
	m.transcriptCursor = 0
	m.transcriptReport = sessionsearch.Report{Query: m.transcriptQuery, Matches: []sessionsearch.Match{}}
	return func() tea.Msg {
		if mon == nil {
			return transcriptSearchMsg{request: request, err: fmt.Errorf("transcript search is unavailable")}
		}
		report, err := mon.Search(context.Background(), query)
		return transcriptSearchMsg{request: request, report: report, err: err}
	}
}

func (m *Model) applyTranscriptSearch(message transcriptSearchMsg) {
	if message.request != m.transcriptRequest {
		return
	}
	m.transcriptLoading = false
	if message.err != nil {
		m.notice = "transcript search failed: " + message.err.Error()
		return
	}
	m.transcriptReport = message.report
	m.transcriptCursor = 0
	m.transcriptOffset = 0
	m.notice = ""
	if len(message.report.Warnings) > 0 {
		m.notice = fmt.Sprintf("search completed with %d warning(s)", len(message.report.Warnings))
	}
}

func (m Model) transcriptContent() string {
	if m.transcriptLoading {
		return titleStyle.Render("TRANSCRIPT SEARCH") + "\n\nSearching provider transcripts…"
	}
	if m.transcriptQuery == "" {
		return titleStyle.Render("TRANSCRIPT SEARCH") + "\n\nPress / to search current and previous user/assistant messages."
	}
	var body strings.Builder
	fmt.Fprintf(&body, "%s  %d match(es), newest first\n", titleStyle.Render("TRANSCRIPT SEARCH"), len(m.transcriptReport.Matches))
	for _, warning := range m.transcriptReport.Warnings {
		fmt.Fprintf(&body, "\nWarning: %s\n", warning)
	}
	if len(m.transcriptReport.Matches) == 0 {
		body.WriteString("\nNo matches.\n")
	}
	width := max(20, m.width-4)
	for index, match := range m.transcriptReport.Matches {
		when := "unknown time"
		if !match.Timestamp.IsZero() {
			when = match.Timestamp.Local().Format("2006-01-02 15:04")
		}
		session := match.SessionID
		if match.SubagentID != "" {
			session += "/" + match.SubagentID
		}
		header := transcriptMatchHeader(when, match, session)
		if index == m.transcriptCursor {
			header = "› " + titleStyle.Render(header)
		} else {
			header = "  " + header
		}
		fmt.Fprintf(&body, "\n%s\n", header)
		if match.Project != "" {
			fmt.Fprintf(&body, "%s\n", muted.Render(match.Project))
		}
		body.WriteString(wrapTranscriptText(match.Snippet, width))
		body.WriteByte('\n')
	}
	return strings.TrimRight(body.String(), "\n")
}

func (m Model) transcriptBody() string {
	content := m.transcriptContent()
	lines := strings.Split(content, "\n")
	visible := max(3, m.height-m.chromeHeight())
	if m.height <= 0 || len(lines) <= visible {
		return content
	}
	contentRows := max(1, visible-1)
	offset := min(max(0, m.transcriptOffset), max(0, len(lines)-contentRows))
	end := min(len(lines), offset+contentRows)
	selected := 0
	if len(m.transcriptReport.Matches) > 0 {
		selected = m.transcriptCursor + 1
	}
	footer := fmt.Sprintf("↑/↓ select  %d/%d  •  lines %d–%d/%d", selected, len(m.transcriptReport.Matches), offset+1, end, len(lines))
	return strings.Join(lines[offset:end], "\n") + "\n" + muted.Render(footer)
}

func (m *Model) moveTranscriptCursor(delta int) {
	if len(m.transcriptReport.Matches) == 0 {
		return
	}
	m.transcriptCursor = min(max(0, m.transcriptCursor+delta), len(m.transcriptReport.Matches)-1)
	m.ensureTranscriptSelectionVisible()
}

func (m *Model) ensureTranscriptSelectionVisible() {
	lines := strings.Split(m.transcriptContent(), "\n")
	selectedLine := 0
	for index, line := range lines {
		if strings.HasPrefix(line, "› ") {
			selectedLine = index
			break
		}
	}
	selectedEnd := len(lines) - 1
	if next := m.transcriptCursor + 1; next < len(m.transcriptReport.Matches) {
		match := m.transcriptReport.Matches[next]
		when := "unknown time"
		if !match.Timestamp.IsZero() {
			when = match.Timestamp.Local().Format("2006-01-02 15:04")
		}
		session := match.SessionID
		if match.SubagentID != "" {
			session += "/" + match.SubagentID
		}
		nextHeader := "  " + transcriptMatchHeader(when, match, session)
		for index := selectedLine + 1; index < len(lines); index++ {
			if lines[index] == nextHeader {
				selectedEnd = index - 1
				break
			}
		}
	}
	visible := max(2, m.height-m.chromeHeight()-1)
	if selectedLine < m.transcriptOffset {
		m.transcriptOffset = selectedLine
	} else if selectedEnd >= m.transcriptOffset+visible {
		if selectedEnd-selectedLine+1 <= visible {
			m.transcriptOffset = selectedEnd - visible + 1
		} else {
			m.transcriptOffset = selectedLine
		}
	}
	m.transcriptOffset = min(max(0, m.transcriptOffset), max(0, len(lines)-visible))
}

func transcriptMatchHeader(when string, match sessionsearch.Match, session string) string {
	return fmt.Sprintf("%s  %s · %s · %s", when, match.Provider, match.Role, session)
}

func (m *Model) openTranscriptDetail() tea.Cmd {
	if m.transcriptCursor < 0 || m.transcriptCursor >= len(m.transcriptReport.Matches) {
		return nil
	}
	match := m.transcriptReport.Matches[m.transcriptCursor]
	for index := range m.sessions {
		session := m.sessions[index]
		matchesSession := session.Provider == match.Provider && session.ID == match.SessionID
		if !matchesSession && session.Provider == match.Provider {
			for _, subagent := range session.Subagents {
				if subagent.ID == match.SessionID || subagent.ID == match.SubagentID {
					matchesSession = true
					break
				}
			}
		}
		if !matchesSession {
			continue
		}
		m.detailSession = &session
		m.detail = true
		m.detailOffset = 0
		m.notice = ""
		return m.startDetailPrompts(session)
	}
	m.notice = "matching session is not available in the index yet"
	return nil
}

func wrapTranscriptText(text string, width int) string {
	width = max(1, width)
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	var lines []string
	line := ""
	for _, word := range words {
		if line != "" && utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) <= width {
			line += " " + word
			continue
		}
		if line != "" {
			lines = append(lines, line)
			line = ""
		}
		runes := []rune(word)
		for len(runes) > width {
			lines = append(lines, string(runes[:width]))
			runes = runes[width:]
		}
		line = string(runes)
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
