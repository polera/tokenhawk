package monitor

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/polera/tokenhawk/internal/anthropiccost"
	"github.com/polera/tokenhawk/internal/config"
	"github.com/polera/tokenhawk/internal/core"
	"github.com/polera/tokenhawk/internal/pricing"
	"github.com/polera/tokenhawk/internal/store"
)

type recordingCostFetcher struct {
	ranges [][2]time.Time
}

func (f *recordingCostFetcher) Fetch(_ context.Context, start, end time.Time) (anthropiccost.Ledger, error) {
	f.ranges = append(f.ranges, [2]time.Time{start, end})
	var covered []time.Time
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		covered = append(covered, day)
	}
	return anthropiccost.Ledger{
		CoveredDays: covered,
		Costs: []core.ReportedCost{{
			Provider: core.Claude, Day: start, Model: "claude-opus-5",
			AmountNanoUSD: 1_000_000_000, Source: anthropiccost.Source,
		}},
	}, nil
}

func TestScanIncrementalAndReconcile(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(claude, "one.jsonl")
	line1 := `{"type":"assistant","sessionId":"one","cwd":"/work","timestamp":"2026-07-14T12:00:00Z","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":10,"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(path, []byte(line1), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ClaudeDir: claude, CodexDir: filepath.Join(root, "codex"), GeminiDir: filepath.Join(root, "gemini"), ActiveWindow: 100 * 365 * 24 * time.Hour, Refresh: 10 * time.Millisecond}
	m := New(cfg, s, prices)
	ctx := context.Background()
	if err := m.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"assistant","sessionId":"one","timestamp":"2026-07-14T12:01:00Z","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":5,"output_tokens":1}}}` + "\n")
	_ = f.Close()
	if err := m.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	sessions, err := m.Sessions(ctx, core.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Totals().Total != 18 {
		t.Fatalf("incremental scan produced %#v", sessions)
	}
	if err := m.ScanProvider(ctx, core.Pi); err != nil {
		t.Fatal(err)
	}
	sessions, err = m.Sessions(ctx, core.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Provider != core.Claude {
		t.Fatalf("provider-only scan removed another provider: %#v", sessions)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := m.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	sessions, err = m.Sessions(ctx, core.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("deleted source retained: %#v", sessions)
	}
}

func TestReportedCostSyncBootstrapsLookbackThenRefreshesTwoDays(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &recordingCostFetcher{}
	m := New(config.Config{AnthropicCostLookbackDays: 5}, s, prices)
	m.costs = fetcher
	if err = m.syncReportedCosts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = m.syncReportedCosts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fetcher.ranges) != 2 {
		t.Fatalf("cost fetch calls = %#v", fetcher.ranges)
	}
	if got := int(fetcher.ranges[0][1].Sub(fetcher.ranges[0][0]).Hours() / 24); got != 5 {
		t.Fatalf("bootstrap fetched %d days, want 5", got)
	}
	if got := int(fetcher.ranges[1][1].Sub(fetcher.ranges[1][0]).Hours() / 24); got != 2 {
		t.Fatalf("refresh fetched %d days, want 2", got)
	}
	costs, days, err := m.ReportedCosts(context.Background())
	if err != nil || len(costs) != 2 || len(days) != 5 {
		t.Fatalf("synced ledger = costs %#v days %d err %v", costs, len(days), err)
	}
}

func TestScanKeepsSubagentUsageSeparateFromParent(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	childDir := filepath.Join(claude, "parent", "subagents")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(claude, "parent.jsonl"), `{"type":"assistant","sessionId":"parent","cwd":"/work","timestamp":"2026-07-14T12:00:00Z","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":100,"output_tokens":20}}}`+"\n")
	mustWriteFile(t, filepath.Join(childDir, "agent-child.jsonl"), `{"type":"assistant","sessionId":"parent","agentId":"child","timestamp":"2026-07-14T12:01:00Z","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":10,"output_tokens":2}}}`+"\n")
	mustWriteFile(t, filepath.Join(childDir, "agent-child.meta.json"), `{"agentType":"Explore"}`)
	s, err := store.Open(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ClaudeDir: claude, CodexDir: filepath.Join(root, "codex"), GeminiDir: filepath.Join(root, "gemini"), ActiveWindow: 100 * 365 * 24 * time.Hour}
	m := New(cfg, s, prices)
	if err = m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	sessions, err := m.Sessions(context.Background(), core.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "parent" || sessions[0].DirectTotals().Total != 120 || sessions[0].Totals().Total != 132 {
		t.Fatalf("parent usage was changed by child: %#v", sessions)
	}
	if len(sessions[0].Subagents) != 1 || sessions[0].Subagents[0].ID != "child" || sessions[0].Subagents[0].Totals().Total != 12 || sessions[0].RunningSubagents() != 1 {
		t.Fatalf("subagent not attached to parent: %#v", sessions[0].Subagents)
	}
}

func TestPiReportedCostSurvivesIncrementalIndexing(t *testing.T) {
	root := t.TempDir()
	piDir := filepath.Join(root, "pi")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(piDir, "session.jsonl")
	first := `{"type":"session","version":3,"id":"pi-session","timestamp":"2026-07-14T12:00:00Z","cwd":"/work/pi"}` + "\n" +
		`{"type":"message","id":"a1","timestamp":"2026-07-14T12:00:01Z","message":{"role":"assistant","provider":"anthropic","model":"claude-sonnet-4-5","usage":{"input":100,"output":20,"cacheRead":50,"totalTokens":170,"cost":{"total":0.01}}}}` + "\n"
	mustWriteFile(t, path, first)
	index, err := store.Open(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	monitor := New(config.Config{PiDir: piDir, ActiveWindow: time.Hour}, index, prices)
	if err = monitor.ScanProvider(context.Background(), core.Pi); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`{"type":"message","id":"a2","timestamp":"2026-07-14T12:00:02Z","message":{"role":"assistant","provider":"anthropic","model":"claude-sonnet-4-5","usage":{"input":20,"output":5,"cacheRead":10,"totalTokens":35,"cost":{"total":0.0025}}}}` + "\n")
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = monitor.ScanProvider(context.Background(), core.Pi); err != nil {
		t.Fatal(err)
	}
	sessions, err := monitor.Sessions(context.Background(), core.Filter{Provider: core.Pi})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("unexpected Pi sessions: %#v", sessions)
	}
	total := sessions[0].Totals()
	if total.Input != 180 || total.CachedInput != 60 || total.Output != 25 || total.Total != 205 || math.Abs(total.CostUSD-0.0125) > 1e-9 || total.PricingStatus != "reported" {
		t.Fatalf("reported incremental Pi usage changed: %#v", total)
	}
}

func TestAgyScanPreservesStatusLineSnapshot(t *testing.T) {
	root := t.TempDir()
	agyDir := filepath.Join(root, "antigravity-cli")
	conversations := filepath.Join(agyDir, "conversations")
	if err := os.MkdirAll(conversations, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(conversations, "agy-session.db")
	mustWriteFile(t, path, "first")

	index, err := store.Open(filepath.Join(root, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m := New(config.Config{AgyDir: agyDir, ActiveWindow: time.Hour}, index, prices)
	ctx := context.Background()
	if err = m.ScanProvider(ctx, core.Agy); err != nil {
		t.Fatal(err)
	}

	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	snapshot := core.Parsed{
		Session: core.Session{
			Provider: core.Agy, ID: "agy-session", Project: "/work/agy",
			StartedAt: now, UpdatedAt: now, SourceHealth: "ok",
			Usage: []core.Usage{{Model: "gemini-3.5-flash", Input: 100, CachedInput: 40, Output: 20, Total: 120, PricingStatus: "priced"}},
		},
		Provider: core.Agy, SourcePath: path, Offset: stat.Size(), Replace: true,
	}
	if err = index.Apply(ctx, snapshot, stat); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString("second"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = m.ScanProvider(ctx, core.Agy); err != nil {
		t.Fatal(err)
	}
	sessions, err := m.Sessions(ctx, core.Filter{Provider: core.Agy})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Project != "/work/agy" || sessions[0].Totals().Total != 120 {
		t.Fatalf("AGY scan discarded status snapshot: %#v", sessions)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
