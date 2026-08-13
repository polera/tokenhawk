package export

import (
	"encoding/csv"
	"encoding/json"
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

func TestSpendExportsContainViewAggregatesAndTimeseries(t *testing.T) {
	since := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	report := SpendReport{
		View: SpendView{
			WindowSpec: "7d", WindowLabel: "last 7 days", Since: &since,
			Until: time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC), Provider: core.Claude,
			Search: "project", Attribution: "last session update UTC day", TimeseriesResolution: "1 day UTC",
		},
		Totals: SpendAggregate{Sessions: 2, Usage: SpendUsage{Input: 100, Total: 120}, Cost: SpendCost{ReportedUSD: 1.25, APIRateUSD: .75, TotalUSD: 2}},
		Timeseries: []SpendPoint{
			{PeriodStart: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), PeriodEnd: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), Sessions: 1, Usage: SpendUsage{Total: 120}, Cost: SpendCost{TotalUSD: 2}},
			{PeriodStart: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), PeriodEnd: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)},
		},
		Providers: []SpendAggregate{{Name: "claude", Sessions: 2, Usage: SpendUsage{Total: 120}, Cost: SpendCost{TotalUSD: 2}}},
		Models:    []SpendAggregate{{Name: "claude-opus", Sessions: 2, Usage: SpendUsage{Total: 120}, Cost: SpendCost{TotalUSD: 2}}},
		Days:      []SpendAggregate{{Name: "2026-08-10", Sessions: 1, Usage: SpendUsage{Total: 120}, Cost: SpendCost{TotalUSD: 2}}},
	}

	jsonPath := filepath.Join(t.TempDir(), "spend.json")
	if err := WriteSpend(jsonPath, "json", report); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var document SpendDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	if document.Kind != "spend" || document.Version != "1" || document.Totals.Cost.TotalUSD != 2 || len(document.Timeseries) != 2 {
		t.Fatalf("unexpected spend JSON document: %#v", document)
	}
	if document.Timeseries[1].Usage.Total != 0 || document.View.TimeseriesResolution != "1 day UTC" {
		t.Fatalf("spend JSON lost zero point or resolution: %#v", document)
	}

	csvPath := filepath.Join(t.TempDir(), "spend.csv")
	if err := WriteSpend(csvPath, "csv", report); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 7 {
		t.Fatalf("spend CSV rows = %d, want header + total + 2 points + 3 breakdowns: %#v", len(rows), rows)
	}
	for index, want := range []string{"total", "timeseries", "timeseries", "provider", "model", "day"} {
		if rows[index+1][0] != want {
			t.Fatalf("spend CSV row %d type = %q, want %q", index+1, rows[index+1][0], want)
		}
	}
	if rows[3][12] != "0" || rows[3][24] != "1 day UTC" {
		t.Fatalf("spend CSV lost zero point or resolution: %#v", rows[3])
	}
}
