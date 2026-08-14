package sessionsearch

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polera/tokenhawk/internal/config"
	"github.com/polera/tokenhawk/internal/core"
	_ "modernc.org/sqlite"
)

func TestSearchesHumanTextAcrossProviderStores(t *testing.T) {
	root := t.TempDir()
	cfg := fixtureConfig(t, root)

	claudePath := filepath.Join(cfg.ClaudeDir, "project", "claude.jsonl")
	claudeLine := `{"type":"user","sessionId":"claude-1","cwd":"/work/claude","timestamp":"2026-08-10T12:00:00Z","message":{"role":"user","content":[{"type":"text","text":"Find the cobalt migration notes"},{"type":"tool_result","content":"cobalt hidden tool result"}]}}`
	mustWrite(t, claudePath, claudeLine+"\n"+claudeLine+"\n")

	codexPath := filepath.Join(cfg.CodexDir, "archived_sessions", "2026", "codex.jsonl")
	mustWrite(t, codexPath, `{"type":"session_meta","timestamp":"2026-08-09T12:00:00Z","payload":{"id":"codex-1","cwd":"/work/codex"}}
{"type":"event_msg","timestamp":"2026-08-09T12:01:00Z","payload":{"type":"agent_message","message":"The cobalt migration is complete"}}
`)

	geminiPath := filepath.Join(cfg.GeminiDir, "project", "chats", "session-gemini.json")
	mustWrite(t, geminiPath, `{"sessionId":"gemini-1","messages":[{"type":"user","timestamp":"2026-08-08T12:00:00Z","content":"cobalt checklist"}]}`)
	mustWrite(t, filepath.Join(cfg.GeminiDir, "project", ".project_root"), "/work/gemini\n")

	piPath := filepath.Join(cfg.PiDir, "pi.jsonl")
	mustWrite(t, piPath, `{"type":"session","id":"pi-1","cwd":"/work/pi","timestamp":"2026-08-07T12:00:00Z"}
{"type":"message","id":"m1","timestamp":"2026-08-07T12:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"cobalt rollout"},{"type":"toolCall","text":"cobalt ignored call"}]}}
`)

	createOpenCodeFixture(t, cfg.OpenCodeDB)

	report, err := Search(context.Background(), cfg, Query{Text: "CoBaLt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Matches) != 5 {
		t.Fatalf("got %d matches, want one per searchable provider: %#v", len(report.Matches), report.Matches)
	}
	providers := map[core.Provider]bool{}
	for _, match := range report.Matches {
		providers[match.Provider] = true
		if strings.Contains(match.Snippet, "ignored") || strings.Contains(match.Snippet, "hidden") {
			t.Fatalf("tool content was searched: %#v", match)
		}
	}
	for _, provider := range []core.Provider{core.Claude, core.Codex, core.Gemini, core.Pi, core.OpenCode} {
		if !providers[provider] {
			t.Errorf("missing %s match", provider)
		}
	}
}

func TestSearchFiltersAndLimitsNewestFirst(t *testing.T) {
	root := t.TempDir()
	cfg := fixtureConfig(t, root)
	path := filepath.Join(cfg.CodexDir, "sessions", "codex.jsonl")
	mustWrite(t, path, `{"type":"session_meta","timestamp":"2026-08-09T12:00:00Z","payload":{"id":"codex-1","cwd":"/work/right"}}
{"type":"event_msg","timestamp":"2026-08-09T12:01:00Z","payload":{"type":"user_message","message":"needle old"}}
{"type":"event_msg","timestamp":"2026-08-10T12:01:00Z","payload":{"type":"agent_message","message":"needle new"}}
`)

	report, err := Search(context.Background(), cfg, Query{
		Text: "needle", Provider: core.Codex, Project: "/work/right", Role: "assistant",
		Since: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Matches) != 1 || report.Matches[0].Snippet != "needle new" {
		t.Fatalf("unexpected filtered matches: %#v", report.Matches)
	}
}

func TestCodexConversationReadsCurrentResponseItems(t *testing.T) {
	cfg := fixtureConfig(t, t.TempDir())
	path := filepath.Join(cfg.CodexDir, "sessions", "codex.jsonl")
	mustWrite(t, path, `{"type":"session_meta","timestamp":"2026-08-11T12:00:00Z","payload":{"id":"codex-current","session_id":"codex-current","cwd":"/work/current","source":"cli"}}
{"type":"response_item","timestamp":"2026-08-11T12:00:01Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>generated runtime details</environment_context>"}]}}
{"type":"response_item","timestamp":"2026-08-11T12:00:02Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Show the current Codex prompt"}]}}
{"type":"response_item","timestamp":"2026-08-11T12:00:03Z","payload":{"type":"function_call","name":"shell","arguments":"current Codex prompt must stay hidden"}}
{"type":"response_item","timestamp":"2026-08-11T12:00:04Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Here is the current Codex response"}]}}
`)

	report, err := Conversation(context.Background(), cfg, core.Codex, "codex-current")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Matches) != 2 {
		t.Fatalf("got %d current Codex messages, want user and assistant only: %#v", len(report.Matches), report.Matches)
	}
	if report.Matches[0].Role != "user" || report.Matches[0].Snippet != "Show the current Codex prompt" || report.Matches[0].Project != "/work/current" {
		t.Fatalf("current Codex user prompt was not decoded: %#v", report.Matches[0])
	}
	if report.Matches[1].Role != "assistant" || report.Matches[1].Snippet != "Here is the current Codex response" {
		t.Fatalf("current Codex assistant response was not decoded: %#v", report.Matches[1])
	}
	for _, match := range report.Matches {
		if strings.Contains(match.Snippet, "environment_context") || strings.Contains(match.Snippet, "must stay hidden") {
			t.Fatalf("generated or tool context leaked into the conversation: %#v", match)
		}
	}
}

func TestCodexConversationPrefersResponseItemsOverDuplicateEvents(t *testing.T) {
	cfg := fixtureConfig(t, t.TempDir())
	path := filepath.Join(cfg.CodexDir, "sessions", "codex.jsonl")
	mustWrite(t, path, `{"type":"session_meta","timestamp":"2026-08-05T12:00:00Z","payload":{"id":"codex-mixed","cwd":"/work/mixed"}}
{"type":"response_item","timestamp":"2026-08-05T12:00:01.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"one mixed prompt"}]}}
{"type":"event_msg","timestamp":"2026-08-05T12:00:01.001Z","payload":{"type":"user_message","message":"one mixed prompt"}}
{"type":"event_msg","timestamp":"2026-08-05T12:00:02.000Z","payload":{"type":"agent_message","message":"one mixed response"}}
{"type":"response_item","timestamp":"2026-08-05T12:00:02.001Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"one mixed response"}]}}
`)

	report, err := Conversation(context.Background(), cfg, core.Codex, "codex-mixed")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Matches) != 2 || report.Matches[0].Snippet != "one mixed prompt" || report.Matches[1].Snippet != "one mixed response" {
		t.Fatalf("dual-format Codex messages were duplicated or lost: %#v", report.Matches)
	}
}

func TestSearchReturnsOnlyNewestHitPerSession(t *testing.T) {
	cfg := fixtureConfig(t, t.TempDir())
	mustWrite(t, filepath.Join(cfg.CodexDir, "sessions", "one.jsonl"), `{"type":"session_meta","timestamp":"2026-08-09T12:00:00Z","payload":{"id":"session-1","cwd":"/work/one"}}
{"type":"event_msg","timestamp":"2026-08-09T12:01:00Z","payload":{"type":"user_message","message":"needle first occurrence"}}
{"type":"event_msg","timestamp":"2026-08-11T12:01:00Z","payload":{"type":"agent_message","message":"needle newest occurrence"}}
`)
	mustWrite(t, filepath.Join(cfg.CodexDir, "sessions", "two.jsonl"), `{"type":"session_meta","timestamp":"2026-08-10T12:00:00Z","payload":{"id":"session-2","cwd":"/work/two"}}
{"type":"event_msg","timestamp":"2026-08-10T12:01:00Z","payload":{"type":"user_message","message":"needle other session"}}
`)

	report, err := Search(context.Background(), cfg, Query{Text: "needle", Provider: core.Codex})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Matches) != 2 {
		t.Fatalf("got %d matches, want one per session: %#v", len(report.Matches), report.Matches)
	}
	if report.Matches[0].SessionID != "session-1" || report.Matches[0].Snippet != "needle newest occurrence" {
		t.Fatalf("newest session hit was not retained: %#v", report.Matches)
	}
	if report.Matches[1].SessionID != "session-2" {
		t.Fatalf("second session was not retained: %#v", report.Matches)
	}
}

func TestPromptsReturnsFullUserMessagesChronologically(t *testing.T) {
	cfg := fixtureConfig(t, t.TempDir())
	path := filepath.Join(cfg.ClaudeDir, "project", "claude.jsonl")
	longPrompt := strings.Repeat("chronological prompt content ", 12)
	mustWrite(t, path, `{"type":"user","sessionId":"claude-1","cwd":"/work/claude","timestamp":"2026-08-10T12:02:00Z","message":{"role":"user","content":"`+longPrompt+`"}}
{"type":"assistant","sessionId":"claude-1","timestamp":"2026-08-10T12:03:00Z","message":{"role":"assistant","content":"assistant text is not a prompt"}}
{"type":"user","sessionId":"claude-1","timestamp":"2026-08-10T12:01:00Z","message":{"role":"user","content":[{"type":"text","text":"first prompt"},{"type":"tool_result","content":"ignored tool result"}]}}
`)
	report, err := Prompts(context.Background(), cfg, core.Claude, "claude-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Matches) != 2 {
		t.Fatalf("got %d prompts: %#v", len(report.Matches), report.Matches)
	}
	if report.Matches[0].Snippet != "first prompt" || report.Matches[1].Snippet != strings.TrimSpace(longPrompt) {
		t.Fatalf("prompts were truncated or misordered: %#v", report.Matches)
	}
	if len(report.Matches[1].Snippet) <= 200 {
		t.Fatalf("full prompt text was unexpectedly truncated: %q", report.Matches[1].Snippet)
	}
}

func TestConversationReturnsCompleteMessagesChronologically(t *testing.T) {
	cfg := fixtureConfig(t, t.TempDir())
	path := filepath.Join(cfg.ClaudeDir, "project", "claude.jsonl")
	mustWrite(t, path, `{"type":"assistant","sessionId":"claude-1","timestamp":"2026-08-10T12:02:00Z","message":{"role":"assistant","content":"Here is the answer"}}
{"type":"user","sessionId":"claude-1","timestamp":"2026-08-10T12:01:00Z","message":{"role":"user","content":[{"type":"text","text":"First question"},{"type":"text","text":"Additional context"},{"type":"tool_result","content":"ignored tool result"}]}}
`)
	report, err := Conversation(context.Background(), cfg, core.Claude, "claude-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Matches) != 2 {
		t.Fatalf("got %d conversation messages: %#v", len(report.Matches), report.Matches)
	}
	if report.Matches[0].Role != "user" || report.Matches[0].Snippet != "First question\n\nAdditional context" {
		t.Fatalf("user text blocks were not presented as one message: %#v", report.Matches[0])
	}
	if report.Matches[1].Role != "assistant" || report.Matches[1].Snippet != "Here is the answer" {
		t.Fatalf("assistant response missing or misordered: %#v", report.Matches[1])
	}
}

func TestRegexSearchMatchesPatternsAndSnippets(t *testing.T) {
	cfg := fixtureConfig(t, t.TempDir())
	path := filepath.Join(cfg.CodexDir, "sessions", "codex.jsonl")
	mustWrite(t, path, `{"type":"session_meta","timestamp":"2026-08-09T12:00:00Z","payload":{"id":"codex-1","cwd":"/work/regex"}}
{"type":"event_msg","timestamp":"2026-08-09T12:01:00Z","payload":{"type":"user_message","message":"run migration 00042_add_index before deploy"}}
{"type":"event_msg","timestamp":"2026-08-09T12:02:00Z","payload":{"type":"agent_message","message":"nothing relevant here"}}
`)

	report, err := Search(context.Background(), cfg, Query{Text: `MIGRATION \d+_\w+`, Regex: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Matches) != 1 {
		t.Fatalf("got %d regex matches: %#v", len(report.Matches), report.Matches)
	}
	if !strings.Contains(report.Matches[0].Snippet, "migration 00042_add_index") {
		t.Fatalf("regex snippet missing hit: %#v", report.Matches[0])
	}

	// Case-sensitive regex must reject the lowercase transcript text.
	report, err = Search(context.Background(), cfg, Query{Text: `MIGRATION \d+`, Regex: true, CaseSensitive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Matches) != 0 {
		t.Fatalf("case-sensitive regex matched unexpectedly: %#v", report.Matches)
	}
}

func TestRegexSearchRejectsInvalidPattern(t *testing.T) {
	cfg := fixtureConfig(t, t.TempDir())
	_, err := Search(context.Background(), cfg, Query{Text: `unbalanced(`, Regex: true})
	if err == nil || !strings.Contains(err.Error(), "invalid regular expression") {
		t.Fatalf("invalid regex was not reported: %v", err)
	}
}

func TestAgySearchDecodesUserAndAssistantTextOnly(t *testing.T) {
	cfg := fixtureConfig(t, t.TempDir())
	createAgyFixture(t, filepath.Join(cfg.AgyDir, "conversations", "agy-1.db"))

	report, err := Search(context.Background(), cfg, Query{Text: "cobalt", Provider: core.Agy})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Warnings) != 0 {
		t.Fatalf("agy fixture produced warnings: %#v", report.Warnings)
	}
	if len(report.Matches) != 1 {
		t.Fatalf("got %d matches, want the newest per session: %#v", len(report.Matches), report.Matches)
	}
	match := report.Matches[0]
	if match.SessionID != "agy-1" || match.Project != "/work/agy" || match.Role != "assistant" {
		t.Fatalf("agy metadata not decoded: %#v", match)
	}
	if !strings.Contains(match.Snippet, "cobalt rollout is complete") {
		t.Fatalf("assistant text not decoded: %#v", match)
	}

	// Reasoning summaries and tool payloads must stay invisible.
	for _, hidden := range []string{"secret reasoning", "hidden tool payload"} {
		report, err = Search(context.Background(), cfg, Query{Text: hidden, Provider: core.Agy})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Matches) != 0 {
			t.Fatalf("non-message AGY content leaked for %q: %#v", hidden, report.Matches)
		}
	}
}

func TestAgyConversationReturnsChronologicalMessages(t *testing.T) {
	cfg := fixtureConfig(t, t.TempDir())
	createAgyFixture(t, filepath.Join(cfg.AgyDir, "conversations", "agy-1.db"))

	report, err := Conversation(context.Background(), cfg, core.Agy, "agy-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Unsupported) != 0 {
		t.Fatalf("agy conversations are still marked unsupported: %#v", report)
	}
	if len(report.Matches) != 2 {
		t.Fatalf("got %d conversation messages: %#v", len(report.Matches), report.Matches)
	}
	if report.Matches[0].Role != "user" || report.Matches[0].Snippet != "check the cobalt migration" {
		t.Fatalf("user prompt missing or misordered: %#v", report.Matches[0])
	}
	if report.Matches[1].Role != "assistant" || !strings.Contains(report.Matches[1].Snippet, "cobalt rollout is complete") {
		t.Fatalf("assistant response missing or misordered: %#v", report.Matches[1])
	}
}

func TestAgyUnreadableDatabaseSurfacesWarningNotFailure(t *testing.T) {
	cfg := fixtureConfig(t, t.TempDir())
	mustWrite(t, filepath.Join(cfg.AgyDir, "conversations", "broken.db"), "not a database")
	report, err := Search(context.Background(), cfg, Query{Text: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Matches) != 0 || len(report.Warnings) != 1 {
		t.Fatalf("broken agy database was not reported as a warning: %#v", report)
	}
}

func TestWritePlainAndJSON(t *testing.T) {
	report := Report{Query: "needle", Matches: []Match{{Provider: core.Codex, SessionID: "s1", Role: "user", Snippet: "a needle"}}}
	var plain bytes.Buffer
	if err := Write(&plain, "plain", report); err != nil || !strings.Contains(plain.String(), "a needle") {
		t.Fatalf("plain output: %q, %v", plain.String(), err)
	}
	var encoded bytes.Buffer
	if err := Write(&encoded, "json", report); err != nil || !strings.Contains(encoded.String(), `"session_id": "s1"`) {
		t.Fatalf("JSON output: %q, %v", encoded.String(), err)
	}
}

func fixtureConfig(t *testing.T, root string) config.Config {
	t.Helper()
	return config.Config{
		ClaudeDir: filepath.Join(root, "claude"), CodexDir: filepath.Join(root, "codex"),
		GeminiDir: filepath.Join(root, "gemini"), AgyDir: filepath.Join(root, "agy"),
		PiDir: filepath.Join(root, "pi"), OpenCodeDB: filepath.Join(root, "opencode", "opencode.db"),
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func createOpenCodeFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE session(id TEXT PRIMARY KEY,directory TEXT NOT NULL,parent_id TEXT);
CREATE TABLE message(id TEXT PRIMARY KEY,session_id TEXT NOT NULL,time_created INTEGER NOT NULL,data TEXT NOT NULL);
CREATE TABLE part(id TEXT PRIMARY KEY,message_id TEXT NOT NULL,data TEXT NOT NULL);
INSERT INTO session VALUES('open-1','/work/open',NULL);
INSERT INTO message VALUES('m1','open-1',1786190400000,'{"role":"assistant"}');
INSERT INTO part VALUES('p1','m1','{"type":"text","text":"cobalt OpenCode result"}');`)
	if err != nil {
		t.Fatal(err)
	}
}

// pbVarint, pbBytes, and pbMessage hand-encode protobuf wire format so the
// AGY fixtures exercise the real payload layout without a schema dependency.
func pbVarint(field int, value uint64) []byte {
	out := binary.AppendUvarint(nil, uint64(field)<<3)
	return binary.AppendUvarint(out, value)
}

func pbBytes(field int, value []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(field)<<3|2)
	out = binary.AppendUvarint(out, uint64(len(value)))
	return append(out, value...)
}

func pbString(field int, value string) []byte { return pbBytes(field, []byte(value)) }

func pbMessage(field int, parts ...[]byte) []byte {
	var inner []byte
	for _, part := range parts {
		inner = append(inner, part...)
	}
	return pbBytes(field, inner)
}

func createAgyFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE steps(idx INTEGER PRIMARY KEY, step_type INTEGER NOT NULL, step_payload BLOB);\n" +
		"CREATE TABLE trajectory_metadata_blob(id TEXT PRIMARY KEY, data BLOB);"); err != nil {
		t.Fatal(err)
	}
	meta := func(seconds uint64) []byte {
		return pbMessage(5, pbMessage(1, pbVarint(1, seconds)))
	}
	userStep := append(pbVarint(1, 14), meta(1785286209)...)
	userStep = append(userStep, pbMessage(19, pbString(2, "check the cobalt migration"))...)
	toolStep := append(pbVarint(1, 15), meta(1785286215)...)
	toolStep = append(toolStep, pbMessage(20,
		pbString(3, "secret reasoning about cobalt"),
		pbMessage(7, pbString(2, "hidden tool payload cobalt")))...)
	responseStep := append(pbVarint(1, 15), meta(1785286230)...)
	responseStep = append(responseStep, pbMessage(20,
		pbString(1, "the cobalt rollout is complete"),
		pbString(3, "secret reasoning trailing"))...)
	for i, payload := range [][]byte{userStep, toolStep, responseStep} {
		if _, err = db.Exec(`INSERT INTO steps(idx, step_type, step_payload) VALUES(?,?,?)`, i, int(payload[1]), payload); err != nil {
			t.Fatal(err)
		}
	}
	blob := pbString(7, "file:///work/agy")
	if _, err = db.Exec(`INSERT INTO trajectory_metadata_blob(id, data) VALUES('main', ?)`, blob); err != nil {
		t.Fatal(err)
	}
}
