package providers

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polera/tokenhawk/internal/core"
)

func TestClaudeIncremental(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "session.jsonl")
	first := `{"type":"user","sessionId":"c1","cwd":"/work/demo","timestamp":"2026-07-14T12:00:00Z"}
{"type":"assistant","sessionId":"c1","timestamp":"2026-07-14T12:00:00Z","message":{"model":"<synthetic>","usage":{}}}
{"type":"assistant","sessionId":"c1","timestamp":"2026-07-14T12:00:01Z","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":10,"output_tokens":20}}}
`
	mustWrite(t, p, first)
	r, err := Parse(p, core.Claude, core.SourceState{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Replace || r.Session.ID != "c1" || len(r.Session.Usage) != 1 {
		t.Fatalf("unexpected parse: %#v", r)
	}
	u := r.Session.Usage[0]
	if u.Input != 140 || u.CachedInput != 40 || u.CacheCreation != 10 || u.Output != 20 || u.Total != 170 {
		t.Fatalf("unexpected usage: %#v", u)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"assistant","sessionId":"c1","timestamp":"2026-07-14T12:00:02Z","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":5,"output_tokens":7}}}` + "\n")
	_ = f.Close()
	r2, err := Parse(p, core.Claude, core.SourceState{SessionID: "c1", Offset: r.Offset})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Replace || r2.Session.Usage[0].Total != 12 {
		t.Fatalf("increment was not isolated: %#v", r2)
	}
}

func TestCodexCumulativeCountersBecomeDeltas(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	mustWrite(t, p, `{"timestamp":"2026-07-14T12:00:00Z","type":"session_meta","payload":{"id":"x1","cwd":"/work/codex"}}
{"timestamp":"2026-07-14T12:00:01Z","type":"turn_context","payload":{"model":"gpt-5"}}
{"timestamp":"2026-07-14T12:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10,"reasoning_output_tokens":3,"total_tokens":110}}}}
{"not":"a recognized record"}
{"timestamp":"2026-07-14T12:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":160,"cached_input_tokens":30,"output_tokens":25,"reasoning_output_tokens":5,"total_tokens":185}}}}
`)
	r, err := Parse(p, core.Codex, core.SourceState{})
	if err != nil {
		t.Fatal(err)
	}
	u := r.Session.Usage[0]
	if u.Input != 160 || u.CachedInput != 30 || u.Output != 25 || u.Reasoning != 5 || u.Total != 185 {
		t.Fatalf("cumulative totals double-counted: %#v", u)
	}
}

func TestClaudeSubagentIsAttachedToParent(t *testing.T) {
	d := filepath.Join(t.TempDir(), "parent-session", "subagents")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "agent-child.jsonl")
	mustWrite(t, p, `{"type":"assistant","sessionId":"parent-session","agentId":"child","timestamp":"2026-07-14T12:00:01Z","message":{"model":"claude-sonnet-5","usage":{"input_tokens":10,"output_tokens":2}}}`+"\n")
	mustWrite(t, filepath.Join(d, "agent-child.meta.json"), `{"agentType":"Explore","description":"must not be indexed"}`)
	r, err := Parse(p, core.Claude, core.SourceState{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Subagent == nil || r.Session.ID != "" || r.Subagent.ID != "child" || r.Subagent.ParentID != "parent-session" || r.Subagent.Name != "Explore" {
		t.Fatalf("unexpected Claude subagent: %#v", r)
	}
	if r.Subagent.Totals().Total != 12 {
		t.Fatalf("unexpected child usage: %#v", r.Subagent.Usage)
	}
}

func TestCodexSubagentUsesChildIDAndParentMetadata(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout-child.jsonl")
	mustWrite(t, p, `{"timestamp":"2026-07-14T12:00:00Z","type":"session_meta","payload":{"session_id":"parent","id":"child","parent_thread_id":"parent","thread_source":"subagent","agent_nickname":"Plato","agent_path":"/root/review","cwd":"/work/codex"}}
{"timestamp":"2026-07-14T12:00:01Z","type":"turn_context","payload":{"model":"gpt-5.6"}}
{"timestamp":"2026-07-14T12:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25}}}}
`)
	r, err := Parse(p, core.Codex, core.SourceState{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Subagent == nil || r.Session.ID != "" || r.Subagent.ID != "child" || r.Subagent.ParentID != "parent" || r.Subagent.Name != "Plato" || r.Subagent.AgentPath != "/root/review" {
		t.Fatalf("unexpected Codex subagent: %#v", r)
	}
	if r.Subagent.Totals().Total != 25 {
		t.Fatalf("unexpected child usage: %#v", r.Subagent.Usage)
	}
}

func TestGeminiTokenCategories(t *testing.T) {
	d := t.TempDir()
	chatDir := filepath.Join(d, "project", "chats")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(d, "project", ".project_root"), "/work/gemini\n")
	p := filepath.Join(chatDir, "session-one.json")
	mustWrite(t, p, `{"sessionId":"g1","startTime":"2026-07-14T12:00:00Z","lastUpdated":"2026-07-14T12:01:00Z","messages":[{"model":"gemini-2.5-pro","tokens":{"input":100,"cached":20,"output":30,"thoughts":5,"tool":2,"total":137}}]}`)
	r, err := Parse(p, core.Gemini, core.SourceState{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Session.Project != "/work/gemini" || r.Session.Usage[0].Total != 137 || r.Session.Usage[0].Reasoning != 5 {
		t.Fatalf("unexpected Gemini parse: %#v", r)
	}
}

func TestPiReportedUsageAndCost(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.jsonl")
	mustWrite(t, p, `{"type":"session","version":3,"id":"pi-session","timestamp":"2026-07-14T12:00:00Z","cwd":"/work/pi"}
{"type":"message","id":"a1","parentId":null,"timestamp":"2026-07-14T12:00:01Z","message":{"role":"assistant","provider":"anthropic","model":"claude-sonnet-4-5","usage":{"input":100,"output":30,"cacheRead":20,"cacheWrite":5,"totalTokens":155,"cost":{"total":0.0123}}}}
`)
	parsed, err := Parse(p, core.Pi, core.SourceState{})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Session.ID != "pi-session" || parsed.Session.Project != "/work/pi" || len(parsed.Session.Usage) != 1 {
		t.Fatalf("unexpected Pi session: %#v", parsed)
	}
	usage := parsed.Session.Usage[0]
	if usage.Model != "anthropic/claude-sonnet-4-5" || usage.Input != 120 || usage.CachedInput != 20 || usage.CacheCreation != 5 || usage.Output != 30 || usage.Total != 155 || usage.CostUSD != 0.0123 || usage.PricingStatus != "reported" {
		t.Fatalf("unexpected Pi usage: %#v", usage)
	}
}

func TestOpenCodeDatabaseSessionsAndSubagents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE session(id TEXT PRIMARY KEY,directory TEXT NOT NULL,parent_id TEXT,title TEXT,agent TEXT,time_created INTEGER NOT NULL,time_updated INTEGER NOT NULL);
CREATE TABLE message(id TEXT PRIMARY KEY,session_id TEXT NOT NULL,time_created INTEGER NOT NULL,data TEXT NOT NULL);
INSERT INTO session VALUES('parent','/work/opencode',NULL,'Main','build',1000,3000);
INSERT INTO session VALUES('child','/work/opencode','parent','Explore','explore',2000,4000);
INSERT INTO message VALUES('m1','parent',2000,'{"role":"assistant","providerID":"openai","modelID":"gpt-5","cost":0.25,"tokens":{"input":100,"output":20,"reasoning":5,"cache":{"read":40,"write":3}}}');
INSERT INTO message VALUES('m2','child',3000,'{"role":"assistant","providerID":"anthropic","modelID":"claude-sonnet-4-5","cost":0.5,"tokens":{"input":200,"output":30,"reasoning":0,"cache":{"read":100,"write":4}}}');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	parsed, sources, err := ParseOpenCodeDB(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Fatalf("got %d OpenCode records: %#v", len(parsed), parsed)
	}
	if len(sources) != 2 {
		t.Fatalf("got %d OpenCode sources: %#v", len(sources), sources)
	}
	var parent core.Session
	var child *core.Subagent
	for _, record := range parsed {
		if record.Subagent != nil {
			child = record.Subagent
		} else {
			parent = record.Session
		}
		if record.Provider != core.OpenCode || !strings.Contains(record.SourcePath, "#session=") || !record.Replace {
			t.Fatalf("invalid OpenCode source record: %#v", record)
		}
	}
	if parent.ID != "parent" || parent.Project != "/work/opencode" || parent.Usage[0].Input != 140 || parent.Usage[0].Reasoning != 5 || parent.Usage[0].CostUSD != 0.25 || parent.Usage[0].PricingStatus != "reported" {
		t.Fatalf("unexpected OpenCode parent: %#v", parent)
	}
	if child == nil || child.ID != "child" || child.ParentID != "parent" || child.Name != "Explore" || child.Totals().CachedInput != 100 {
		t.Fatalf("unexpected OpenCode subagent: %#v", child)
	}
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
