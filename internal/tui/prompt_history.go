package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/polera/tokenhawk/internal/core"
	"github.com/polera/tokenhawk/internal/sessionsearch"
)

type detailPromptsMsg struct {
	request int
	report  sessionsearch.Report
	err     error
}

func (m *Model) startDetailPrompts(session core.Session) tea.Cmd {
	m.detailPromptRequest++
	request := m.detailPromptRequest
	m.detailPromptProvider = session.Provider
	m.detailPromptSessionID = session.ID
	m.detailPromptsLoading = true
	m.detailPromptsError = ""
	m.detailPrompts = sessionsearch.Report{Matches: []sessionsearch.Match{}}
	mon := m.monitor
	return func() tea.Msg {
		if mon == nil {
			return detailPromptsMsg{request: request, err: fmt.Errorf("session conversation is unavailable")}
		}
		report, err := mon.Conversation(context.Background(), session.Provider, session.ID)
		return detailPromptsMsg{request: request, report: report, err: err}
	}
}

func (m *Model) applyDetailPrompts(message detailPromptsMsg) {
	if message.request != m.detailPromptRequest || !m.detail {
		return
	}
	m.detailPromptsLoading = false
	if message.err != nil {
		m.detailPromptsError = message.err.Error()
		return
	}
	m.detailPrompts = message.report
	m.detailPromptsError = ""
	if m.detailSearch != "" {
		m.detailSearchMatch = 0
		m.jumpToDetailSearchMatch()
	}
}

func (m *Model) closeDetail() {
	m.detail = false
	m.detailSession = nil
	m.detailOffset = 0
	m.detailSearch = ""
	m.detailSearchDraft = ""
	m.detailSearching = false
	m.detailSearchMatch = 0
	m.detailNotice = ""
	m.detailPromptsLoading = false
	m.detailPromptsError = ""
	m.detailPrompts = sessionsearch.Report{}
	m.detailPromptProvider = ""
	m.detailPromptSessionID = ""
	m.detailPromptRequest++
}

func (m Model) updateDetailSearch(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		m.detailSearch = strings.TrimSpace(m.detailSearchDraft)
		m.detailSearching = false
		m.detailSearchMatch = 0
		m.jumpToDetailSearchMatch()
	case "esc":
		m.detailSearching = false
		m.detailSearchDraft = ""
	case "backspace":
		runes := []rune(m.detailSearchDraft)
		if len(runes) > 0 {
			m.detailSearchDraft = string(runes[:len(runes)-1])
		}
	default:
		if key.Key().Text != "" {
			m.detailSearchDraft += key.Key().Text
		}
	}
	return m, nil
}

func (m *Model) moveDetailSearch(delta int) {
	matches := m.detailSearchLines()
	if len(matches) == 0 {
		return
	}
	m.detailSearchMatch = (m.detailSearchMatch + delta + len(matches)) % len(matches)
	m.jumpToDetailSearchMatch()
}

func (m *Model) jumpToDetailSearchMatch() {
	matches := m.detailSearchLines()
	if len(matches) == 0 {
		m.detailOffset = 0
		m.detailSearchMatch = 0
		return
	}
	m.detailSearchMatch = min(max(0, m.detailSearchMatch), len(matches)-1)
	m.detailOffset = min(matches[m.detailSearchMatch], m.detailMaxOffset())
}

func (m Model) detailSearchPosition() (int, int) {
	matches := m.detailSearchLines()
	if len(matches) == 0 {
		return 0, 0
	}
	return min(max(0, m.detailSearchMatch), len(matches)-1) + 1, len(matches)
}

func (m Model) detailSearchLines() []int {
	query := strings.ToLower(strings.TrimSpace(m.detailSearch))
	if query == "" {
		return nil
	}
	var matches []int
	for index, line := range strings.Split(m.detailContent(), "\n") {
		if strings.Contains(strings.ToLower(ansi.Strip(line)), query) {
			matches = append(matches, index)
		}
	}
	return matches
}

func (m Model) detailPromptContent(session core.Session) string {
	var body strings.Builder
	body.WriteString(titleStyle.Render("SESSION CONVERSATION") + "\n")
	fmt.Fprintf(&body, "%s\n\n", muted.Render(fmt.Sprintf("%s · %s", session.Provider, session.ID)))
	if m.detailPromptProvider != session.Provider || m.detailPromptSessionID != session.ID {
		body.WriteString(muted.Render("Conversation has not been loaded for this session.") + "\n")
		return body.String()
	}
	if m.detailPromptsLoading {
		body.WriteString(muted.Render("Loading conversation from the provider transcript…") + "\n")
		return body.String()
	}
	if m.detailPromptsError != "" {
		body.WriteString(alarmStyle.Render("Conversation unavailable: "+m.detailPromptsError) + "\n")
		return body.String()
	}
	if len(m.detailPrompts.Unsupported) > 0 {
		fmt.Fprintf(&body, "%s\n", muted.Render(fmt.Sprintf("Unavailable: %s transcript content uses an unsupported private format.", m.detailPrompts.Unsupported[0])))
		return body.String()
	}
	for _, warning := range m.detailPrompts.Warnings {
		fmt.Fprintf(&body, "%s\n", muted.Render("Warning: "+warning))
	}
	if len(m.detailPrompts.Matches) == 0 {
		body.WriteString(muted.Render("No user or assistant messages were found for this session.") + "\n")
		return body.String()
	}
	userMessages, assistantMessages := 0, 0
	for _, message := range m.detailPrompts.Matches {
		switch message.Role {
		case "user":
			userMessages++
		case "assistant":
			assistantMessages++
		}
	}
	fmt.Fprintf(&body, "%s\n\n", muted.Render(fmt.Sprintf(
		"%d messages · %d user / %d assistant · oldest first",
		len(m.detailPrompts.Matches), userMessages, assistantMessages,
	)))
	width := 74
	if m.width > 0 {
		width = max(1, m.width-6)
	}
	for _, message := range m.detailPrompts.Matches {
		when := "unknown time"
		if !message.Timestamp.IsZero() {
			when = message.Timestamp.Local().Format("Jan 2, 2006 at 3:04:05 PM")
		}
		source := "parent session"
		if message.SubagentID != "" {
			source = "subagent " + message.SubagentID
		}
		speaker := strings.ToUpper(message.Role)
		speakerStyle := titleStyle
		if message.Role == "user" {
			speaker = "YOU"
		} else if message.Role == "assistant" {
			speaker = "ASSISTANT"
			speakerStyle = good.Bold(true)
		}
		if speaker == "" {
			speaker = "MESSAGE"
		}
		fmt.Fprintf(&body, "%s %s\n", muted.Render("╭─"), speakerStyle.Render(speaker)+" "+muted.Render("· "+when+" · "+source))
		wrapped := wrapPromptText(message.Snippet, width)
		for _, line := range strings.Split(wrapped, "\n") {
			fmt.Fprintf(&body, "%s %s\n", muted.Render("│"), line)
		}
		fmt.Fprintf(&body, "%s\n\n", muted.Render("╰─"))
	}
	return body.String()
}

// wrapPromptText wraps long transcript lines without discarding the author's
// paragraphs, lists, code layout, or intentional blank lines.
func wrapPromptText(text string, width int) string {
	width = max(1, width)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(strings.Trim(text, "\n"), "\n")
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			wrapped = append(wrapped, "")
			continue
		}
		runes := []rune(line)
		for len(runes) > width {
			wrapped = append(wrapped, string(runes[:width]))
			runes = runes[width:]
		}
		wrapped = append(wrapped, string(runes))
	}
	return strings.Join(wrapped, "\n")
}
