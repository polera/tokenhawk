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

func TestTranscriptSearchViewAcceptsQueryAndRendersResults(t *testing.T) {
	m := New(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	updated, _ = m.Update(key("4"))
	m = updated.(Model)
	if m.tab != transcriptTab || !m.transcriptInput {
		t.Fatalf("4 did not open the transcript query: tab=%d input=%v", m.tab, m.transcriptInput)
	}
	for _, char := range []string{"n", "e", "e", "d", "l", "e"} {
		updated, _ = m.Update(key(char))
		m = updated.(Model)
	}
	updated, cmd := m.Update(key("enter"))
	m = updated.(Model)
	if cmd == nil || !m.transcriptLoading || m.transcriptQuery != "needle" {
		t.Fatalf("query did not start: loading=%v query=%q cmd=%v", m.transcriptLoading, m.transcriptQuery, cmd)
	}
	request := m.transcriptRequest
	result := sessionsearch.Match{
		Provider: core.Codex, SessionID: "session-1", SubagentID: "agent-1", Project: "/work/demo",
		Timestamp: time.Date(2026, 8, 11, 12, 30, 0, 0, time.Local), Role: "assistant", Snippet: "the needle appears here",
	}
	updated, _ = m.Update(transcriptSearchMsg{request: request, report: sessionsearch.Report{Query: "needle", Matches: []sessionsearch.Match{result}}})
	m = updated.(Model)
	view := m.dashboard()
	for _, want := range []string{"4 Search", "1 transcript matches", "query: needle", "session-1/agent-1", "/work/demo", "the needle appears here"} {
		if !strings.Contains(view, want) {
			t.Fatalf("search view missing %q:\n%s", want, view)
		}
	}
}

func TestTranscriptSearchIgnoresStaleResultsAndRerunsForProvider(t *testing.T) {
	m := New(nil)
	m.tab = transcriptTab
	m.transcriptQuery = "needle"
	first := m.startTranscriptSearch()
	if first == nil {
		t.Fatal("first search returned no command")
	}
	firstRequest := m.transcriptRequest
	updated, cmd := m.Update(key("p"))
	m = updated.(Model)
	if cmd == nil || m.provider != core.Claude || m.transcriptRequest == firstRequest {
		t.Fatalf("provider did not rerun search: provider=%s request=%d", m.provider, m.transcriptRequest)
	}
	updated, _ = m.Update(transcriptSearchMsg{request: firstRequest, report: sessionsearch.Report{Matches: []sessionsearch.Match{{Snippet: "stale"}}}})
	m = updated.(Model)
	if !m.transcriptLoading || len(m.transcriptReport.Matches) != 0 {
		t.Fatalf("stale result replaced current search: loading=%v report=%#v", m.transcriptLoading, m.transcriptReport)
	}
}

func TestTranscriptResultsNavigateToSessionDetailsAndBack(t *testing.T) {
	m := New(nil)
	m.width, m.height, m.tab = 100, 30, transcriptTab
	m.transcriptQuery = "needle"
	m.sessions = []core.Session{
		{Provider: core.Codex, ID: "session-1", Project: "/work/one", SourceHealth: "ok"},
		{Provider: core.Codex, ID: "session-2", Project: "/work/two", SourceHealth: "ok"},
	}
	m.transcriptReport = sessionsearch.Report{Query: "needle", Matches: []sessionsearch.Match{
		{Provider: core.Codex, SessionID: "session-1", Role: "user", Snippet: "first needle"},
		{Provider: core.Codex, SessionID: "session-2", Role: "assistant", Snippet: "second needle"},
	}}
	updated, _ := m.Update(key("j"))
	m = updated.(Model)
	if m.transcriptCursor != 1 || !strings.Contains(m.transcriptContent(), "› ") {
		t.Fatalf("down did not select the second result: cursor=%d\n%s", m.transcriptCursor, m.transcriptContent())
	}
	updated, _ = m.Update(key("enter"))
	m = updated.(Model)
	if !m.detail || !m.detailPromptsLoading || m.detailSession == nil || m.detailSession.ID != "session-2" || !strings.Contains(m.detailContent(), "SESSION session-2") {
		t.Fatalf("enter did not open the selected session: detail=%v session=%#v\n%s", m.detail, m.detailSession, m.detailContent())
	}
	updated, _ = m.Update(key("esc"))
	m = updated.(Model)
	if m.detail || m.detailSession != nil || m.tab != transcriptTab || m.transcriptCursor != 1 {
		t.Fatalf("esc did not return to the selected search result: detail=%v tab=%d cursor=%d", m.detail, m.tab, m.transcriptCursor)
	}
}

func TestTranscriptDetailReportsSessionMissingFromIndex(t *testing.T) {
	m := New(nil)
	m.tab = transcriptTab
	m.transcriptQuery = "needle"
	m.transcriptReport = sessionsearch.Report{Matches: []sessionsearch.Match{{Provider: core.Codex, SessionID: "missing"}}}
	updated, _ := m.Update(key("enter"))
	m = updated.(Model)
	if m.detail || !strings.Contains(m.notice, "not available in the index") {
		t.Fatalf("missing session did not report the problem: detail=%v notice=%q", m.detail, m.notice)
	}
}

func TestTranscriptSelectionScrollsIntoView(t *testing.T) {
	m := New(nil)
	m.width, m.height, m.tab = 80, 18, transcriptTab
	m.transcriptQuery = "needle"
	for index := 0; index < 12; index++ {
		m.transcriptReport.Matches = append(m.transcriptReport.Matches, sessionsearch.Match{
			Provider: core.Codex, SessionID: fmt.Sprintf("session-%02d", index), Role: "user", Snippet: fmt.Sprintf("needle result %02d", index),
		})
	}
	updated, _ := m.Update(key("G"))
	m = updated.(Model)
	if m.transcriptCursor != 11 || m.transcriptOffset == 0 || !strings.Contains(m.transcriptBody(), "needle result 11") {
		t.Fatalf("last result was not brought into view: cursor=%d offset=%d\n%s", m.transcriptCursor, m.transcriptOffset, m.transcriptBody())
	}
}

func TestTranscriptSearchSilentlyIgnoresUnsupportedProviderAndUsesCompactTabs(t *testing.T) {
	m := New(nil)
	m.width, m.height, m.tab = 50, 24, transcriptTab
	m.transcriptQuery = "needle"
	m.transcriptRequest = 1
	m.applyTranscriptSearch(transcriptSearchMsg{request: 1, report: sessionsearch.Report{Query: "needle", Unsupported: []core.Provider{core.Agy}}})
	view := m.dashboard()
	if !strings.Contains(view, "4?") || !strings.Contains(view, "No matches.") {
		t.Fatalf("empty search or compact tabs missing:\n%s", view)
	}
	if strings.Contains(strings.ToLower(view), "agy") || m.notice != "" {
		t.Fatalf("unsupported provider was announced:\n%s\nnotice=%q", view, m.notice)
	}
}

func TestWrapTranscriptTextHonorsWidth(t *testing.T) {
	wrapped := wrapTranscriptText("one two three extraordinarilylongword five", 9)
	for _, line := range strings.Split(wrapped, "\n") {
		if len([]rune(line)) > 9 {
			t.Fatalf("line exceeded width: %q", line)
		}
	}
}
