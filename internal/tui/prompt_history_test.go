package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/polera/tokenhawk/internal/core"
	"github.com/polera/tokenhawk/internal/sessionsearch"
)

func TestDetailConversationRendersUserAndAssistantMessages(t *testing.T) {
	session := core.Session{Provider: core.Codex, ID: "session-1", Project: "/work/demo", SourceHealth: "ok"}
	m := New(nil)
	m.width, m.height = 80, 40
	m.sessions = []core.Session{session}
	m.detailSession = &session
	m.detail = true
	m.detailPromptProvider = core.Codex
	m.detailPromptSessionID = session.ID
	m.detailPrompts = sessionsearch.Report{Matches: []sessionsearch.Match{
		{Provider: core.Codex, SessionID: session.ID, Role: "user", Timestamp: time.Date(2026, 8, 11, 10, 0, 0, 0, time.Local), Snippet: "first prompt\n\n  indented detail"},
		{Provider: core.Codex, SessionID: session.ID, Role: "assistant", SubagentID: "agent-1", Timestamp: time.Date(2026, 8, 11, 11, 0, 0, 0, time.Local), Snippet: "assistant response"},
	}}
	view := m.detailContent()
	first, second := strings.Index(view, "first prompt"), strings.Index(view, "assistant response")
	if first < 0 || second < first {
		t.Fatalf("detail did not include the chronological conversation:\n%s", view)
	}
	for _, want := range []string{"SESSION TOTAL", "SESSION CONVERSATION", "2 messages · 1 user / 1 assistant · oldest first", "YOU", "ASSISTANT", "Aug 11, 2026 at 10:00:00 AM", "parent session", "subagent agent-1", "indented detail"} {
		if !strings.Contains(view, want) {
			t.Fatalf("conversation missing %q:\n%s", want, view)
		}
	}
	if got := wrapPromptText("first prompt\n\n  indented detail", 80); got != "first prompt\n\n  indented detail" {
		t.Fatalf("conversation formatting was flattened: %q", got)
	}
}

func TestOpeningSessionDetailStartsPromptHistoryLoad(t *testing.T) {
	session := core.Session{Provider: core.Codex, ID: "session-1", Active: true}
	m := New(nil)
	m.width, m.height = 80, 30
	m.sessions = []core.Session{session}
	m.resize()
	updated, cmd := m.Update(key("enter"))
	m = updated.(Model)
	if cmd == nil || !m.detail || !m.detailPromptsLoading || m.detailPromptSessionID != session.ID {
		t.Fatalf("opening detail did not start prompt history: detail=%v loading=%v session=%q", m.detail, m.detailPromptsLoading, m.detailPromptSessionID)
	}
}

func TestDetailPromptHistoryPageKeysReachLaterPages(t *testing.T) {
	session := core.Session{Provider: core.Codex, ID: "session-1"}
	m := New(nil)
	m.width, m.height = 60, 12
	m.sessions = []core.Session{session}
	m.detailSession = &session
	m.detail = true
	m.detailPromptProvider = session.Provider
	m.detailPromptSessionID = session.ID
	for index := 0; index < 8; index++ {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		m.detailPrompts.Matches = append(m.detailPrompts.Matches, sessionsearch.Match{Role: role, Snippet: strings.Repeat("message body ", 8)})
	}
	if !strings.Contains(m.detailView(), "page 1/") {
		t.Fatalf("initial view did not identify the first page:\n%s", m.detailView())
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = updated.(Model)
	if m.detailOffset != m.detailViewport() || !strings.Contains(m.detailView(), "page 2/") {
		t.Fatalf("page down did not advance exactly one page: offset=%d\n%s", m.detailOffset, m.detailView())
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = updated.(Model)
	if m.detailOffset != 2*m.detailViewport() || !strings.Contains(m.detailView(), "page 3/") {
		t.Fatalf("second page down did not reach page three: offset=%d\n%s", m.detailOffset, m.detailView())
	}
}

func TestSessionDetailsAndConversationUseOneScrollWithoutPToggle(t *testing.T) {
	session := core.Session{Provider: core.Codex, ID: "session-1"}
	m := New(nil)
	m.width, m.height = 60, 8
	m.sessions = []core.Session{session}
	m.detailSession = &session
	m.detail = true
	m.detailPromptProvider = session.Provider
	m.detailPromptSessionID = session.ID
	for index := 0; index < 8; index++ {
		m.detailPrompts.Matches = append(m.detailPrompts.Matches, sessionsearch.Match{Role: "user", Snippet: strings.Repeat("message body ", 6)})
	}
	content := m.detailContent()
	if !strings.Contains(content, "SESSION TOTAL") || !strings.Contains(content, "SESSION CONVERSATION") {
		t.Fatalf("detail did not combine usage and conversation:\n%s", content)
	}
	updated, _ := m.Update(key("j"))
	m = updated.(Model)
	if m.detailOffset != 1 {
		t.Fatalf("detail did not use its single scroll offset: %d", m.detailOffset)
	}
	updated, _ = m.Update(key("p"))
	m = updated.(Model)
	if !m.detail || m.detailOffset != 1 || !strings.Contains(m.detailContent(), "SESSION CONVERSATION") {
		t.Fatalf("p changed the combined detail view: detail=%v offset=%d", m.detail, m.detailOffset)
	}
}

func TestDetailSlashSearchJumpsAndCyclesThroughMatches(t *testing.T) {
	session := core.Session{Provider: core.Codex, ID: "session-1"}
	m := New(nil)
	m.width, m.height = 60, 10
	m.sessions = []core.Session{session}
	m.detailSession = &session
	m.detail = true
	m.detailPromptProvider = session.Provider
	m.detailPromptSessionID = session.ID
	m.detailPrompts.Matches = []sessionsearch.Match{
		{Role: "user", Snippet: "first needle"},
		{Role: "assistant", Snippet: "second needle"},
	}
	updated, _ := m.Update(key("/"))
	m = updated.(Model)
	if !m.detailSearching || !strings.Contains(m.detailView(), "Enter find") {
		t.Fatalf("slash did not open detail search:\n%s", m.detailView())
	}
	for _, char := range []string{"n", "e", "e", "d", "l", "e"} {
		updated, _ = m.Update(key(char))
		m = updated.(Model)
	}
	updated, _ = m.Update(key("enter"))
	m = updated.(Model)
	firstOffset := m.detailOffset
	if m.detailSearching || m.detailSearch != "needle" || firstOffset == 0 || !strings.Contains(m.detailView(), "match 1/2") || !strings.Contains(m.detailView(), "first needle") {
		t.Fatalf("detail search did not select its first match: offset=%d\n%s", firstOffset, m.detailView())
	}
	updated, _ = m.Update(key("n"))
	m = updated.(Model)
	if !strings.Contains(m.detailView(), "match 2/2") || !strings.Contains(m.detailView(), "second needle") {
		t.Fatalf("n did not select the next detail match: offset=%d\n%s", m.detailOffset, m.detailView())
	}
	updated, _ = m.Update(key("N"))
	m = updated.(Model)
	if !strings.Contains(m.detailView(), "match 1/2") {
		t.Fatalf("N did not return to the previous detail match: offset=%d\n%s", m.detailOffset, m.detailView())
	}
}

func TestEscapeCancelsDetailSearchBeforeClosingDetail(t *testing.T) {
	session := core.Session{Provider: core.Codex, ID: "session-1"}
	m := New(nil)
	m.sessions = []core.Session{session}
	m.detailSession = &session
	m.detail = true
	updated, _ := m.Update(key("/"))
	m = updated.(Model)
	updated, _ = m.Update(key("esc"))
	m = updated.(Model)
	if !m.detail || m.detailSearching {
		t.Fatalf("escape did not cancel detail search: detail=%v searching=%v", m.detail, m.detailSearching)
	}
	updated, _ = m.Update(key("esc"))
	m = updated.(Model)
	if m.detail {
		t.Fatal("escape did not close detail after search was canceled")
	}
}

func TestConversationFinalPageStaysBottomAligned(t *testing.T) {
	session := core.Session{Provider: core.Codex, ID: "session-1"}
	m := New(nil)
	m.width, m.height = 60, 12
	m.sessions = []core.Session{session}
	m.detailSession = &session
	m.detail = true
	m.detailPromptProvider = session.Provider
	m.detailPromptSessionID = session.ID
	for index := 0; index < 4; index++ {
		m.detailPrompts.Matches = append(m.detailPrompts.Matches, sessionsearch.Match{
			Role: "user", Snippet: strings.Repeat("short message ", 4),
		})
	}
	contentLines := strings.Split(m.detailContent(), "\n")
	if len(contentLines) <= m.detailViewport() || len(contentLines)%m.detailViewport() == 0 {
		t.Fatalf("test conversation does not have a partial final page: lines=%d viewport=%d", len(contentLines), m.detailViewport())
	}
	updated, _ := m.Update(key("G"))
	m = updated.(Model)
	wantOffset := len(contentLines) - m.detailViewport()
	if m.detailOffset != wantOffset {
		t.Fatalf("final page was not bottom-aligned: offset=%d want=%d", m.detailOffset, wantOffset)
	}
	if got := len(strings.Split(m.detailView(), "\n")); got != m.detailViewport()+1 {
		t.Fatalf("final page left empty terminal rows: rendered=%d want=%d", got, m.detailViewport()+1)
	}
	pages := (len(contentLines) + m.detailViewport() - 1) / m.detailViewport()
	if !strings.Contains(m.detailView(), fmt.Sprintf("page %d/%d", pages, pages)) {
		t.Fatalf("bottom-aligned final page has the wrong page number:\n%s", m.detailView())
	}
}

func TestDetailExportUsesFullLoadedConversation(t *testing.T) {
	session := core.Session{Provider: core.Codex, ID: "session-1"}
	m := New(nil)
	m.detailPromptProvider = session.Provider
	m.detailPromptSessionID = session.ID
	m.detailPrompts.Matches = []sessionsearch.Match{
		{Role: "user", Timestamp: time.Unix(1, 0), Snippet: "include this prompt"},
		{Role: "assistant", Timestamp: time.Unix(2, 0), Snippet: "include this response"},
		{Role: "user", SubagentID: "agent-1", Timestamp: time.Unix(3, 0), Snippet: "include subagent prompt"},
	}
	conversation, loaded := m.detailExportConversation(session)
	if !loaded {
		t.Fatal("loaded conversation was reported unavailable")
	}
	if len(conversation) != 3 || conversation[0].Role != "user" || conversation[1].Role != "assistant" || conversation[1].Text != "include this response" || conversation[2].SubagentID != "agent-1" {
		t.Fatalf("detail export selected the wrong transcript content: %#v", conversation)
	}
	m.detailPromptsLoading = true
	if conversation, loaded = m.detailExportConversation(session); loaded || len(conversation) != 0 {
		t.Fatalf("unavailable conversation should be omitted: %#v", conversation)
	}
}

func TestDetailExportFailsRatherThanSilentlyOmittingUnavailableConversation(t *testing.T) {
	session := core.Session{Provider: core.Codex, ID: "session-1"}
	m := New(nil)
	message := m.exportDetail("json", session)().(exportMsg)
	if message.err == nil || !strings.Contains(message.err.Error(), "conversation is unavailable") {
		t.Fatalf("export did not report unavailable conversation: %v", message.err)
	}
}

func TestDetailExportStatusRendersBeforeReturningToList(t *testing.T) {
	session := core.Session{Provider: core.Codex, ID: "session-1"}
	m := New(nil)
	m.width, m.height = 80, 30
	m.sessions = []core.Session{session}
	m.detailSession = &session
	m.detail = true
	updated, cmd := m.Update(key("e"))
	m = updated.(Model)
	if cmd == nil || !strings.Contains(m.detailView(), "exporting JSON…") {
		t.Fatalf("detail did not show export progress:\n%s", m.detailView())
	}
	updated, _ = m.Update(exportMsg{path: "/tmp/session.json", detail: true})
	m = updated.(Model)
	if m.notice != "" || !strings.Contains(m.detailView(), "exported /tmp/session.json") {
		t.Fatalf("detail did not show export success: notice=%q\n%s", m.notice, m.detailView())
	}
	updated, _ = m.Update(key("esc"))
	m = updated.(Model)
	if m.detail || m.detailNotice != "" || m.notice != "" {
		t.Fatalf("detail export status leaked into the session list: detail=%v detailNotice=%q notice=%q", m.detail, m.detailNotice, m.notice)
	}
}

func TestEscapeClosesCombinedSessionDetail(t *testing.T) {
	session := core.Session{Provider: core.Codex, ID: "session-1"}
	m := New(nil)
	m.sessions = []core.Session{session}
	m.detailSession = &session
	m.detail = true
	updated, _ := m.Update(key("esc"))
	m = updated.(Model)
	if m.detail {
		t.Fatal("escape should close session details")
	}
}

func TestDetailPromptLoadIsInvalidatedWhenDetailCloses(t *testing.T) {
	session := core.Session{Provider: core.Codex, ID: "session-1"}
	m := New(nil)
	m.detail = true
	cmd := m.startDetailPrompts(session)
	if cmd == nil || !m.detailPromptsLoading {
		t.Fatal("prompt load did not start")
	}
	request := m.detailPromptRequest
	m.closeDetail()
	m.applyDetailPrompts(detailPromptsMsg{request: request, report: sessionsearch.Report{Matches: []sessionsearch.Match{{Snippet: "stale prompt"}}}})
	if m.detail || m.detailPromptsLoading || len(m.detailPrompts.Matches) != 0 {
		t.Fatalf("closed detail accepted a stale prompt result: %#v", m.detailPrompts)
	}
}
