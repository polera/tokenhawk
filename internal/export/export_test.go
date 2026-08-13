package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polera/tokenhawk/internal/core"
)

func TestExportsContainUsageMetadataOnly(t *testing.T) {
	s := []core.Session{{
		Provider: core.Claude, ID: "id", Project: "/work", StartedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
		Usage:     []core.Usage{{Model: "m", Input: 1, Total: 2, PricingStatus: "unpriced"}},
		Subagents: []core.Subagent{{ID: "child", ParentID: "id", Name: "Explore", Running: true, Status: "running", Usage: []core.Usage{{Model: "child-model", Input: 3, Total: 4, CostUSD: .125, PricingStatus: "priced"}}}},
	}}
	for _, format := range []string{"json", "csv"} {
		p := filepath.Join(t.TempDir(), "out."+format)
		if err := Write(p, format, s); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		if !strings.Contains(text, "session_id") || strings.Contains(text, "prompt") || strings.Contains(text, "response") {
			t.Fatalf("unsafe or invalid %s export: %s", format, text)
		}
		if !strings.Contains(text, "api_cost_usd") || strings.Contains(text, "estimated_cost_usd") {
			t.Fatalf("%s export did not use the API-rate cost field: %s", format, text)
		}
		if format == "json" && (!strings.Contains(text, `"version": "2"`) || !strings.Contains(text, `"cost_basis": "public API list-rate USD"`)) {
			t.Fatalf("JSON export did not declare its API-rate cost basis: %s", text)
		}
		for _, want := range []string{"child", "child-model", "0.125"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s export omitted subagent pricing field %q: %s", format, want, text)
			}
		}
	}
}

func TestDetailExportsIncludeFullConversation(t *testing.T) {
	session := core.Session{
		Provider: core.Codex, ID: "session-1", Project: "/work/demo", Active: true, SourceHealth: "ok",
		Usage: []core.Usage{{Model: "model", Input: 10, Total: 12}},
	}
	conversation := []Message{
		{Timestamp: time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC), Role: "user", Text: "parent prompt\nwith context"},
		{Timestamp: time.Date(2026, 8, 11, 11, 0, 0, 0, time.UTC), SubagentID: "agent-1", Role: "assistant", Text: "assistant response"},
	}
	for _, format := range []string{"json", "csv"} {
		t.Run(format, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "detail."+format)
			if err := WriteDetail(path, format, session, conversation); err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(contents)
			for _, want := range []string{"parent prompt", "with context", "assistant response", "assistant", "agent-1"} {
				if !strings.Contains(text, want) {
					t.Fatalf("detail %s export omitted %q:\n%s", format, want, text)
				}
			}
			if format == "json" && (!strings.Contains(text, `"conversation"`) || strings.Contains(text, `"prompts"`)) {
				t.Fatalf("detail JSON has no conversation collection:\n%s", text)
			}
			if format == "csv" && (!strings.Contains(text, "message_text") || !strings.Contains(text, ",message,")) {
				t.Fatalf("detail CSV has no conversation columns or rows:\n%s", text)
			}
		})
	}
}
