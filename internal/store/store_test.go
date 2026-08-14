package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/polera/tokenhawk/internal/anthropiccost"
	"github.com/polera/tokenhawk/internal/core"
)

func TestOpeningLegacyIndexRebuildsMergedUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE sources(path TEXT PRIMARY KEY,provider TEXT NOT NULL,session_id TEXT NOT NULL,size INTEGER NOT NULL,mtime_ns INTEGER NOT NULL,offset INTEGER NOT NULL,parser_state TEXT NOT NULL DEFAULT '');
CREATE TABLE sessions(provider TEXT NOT NULL,id TEXT NOT NULL,project TEXT NOT NULL DEFAULT '',started_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,source_health TEXT NOT NULL DEFAULT 'ok',PRIMARY KEY(provider,id));
CREATE TABLE usage(provider TEXT NOT NULL,session_id TEXT NOT NULL,model TEXT NOT NULL,input INTEGER NOT NULL,cached_input INTEGER NOT NULL,cache_creation INTEGER NOT NULL,output INTEGER NOT NULL,reasoning INTEGER NOT NULL,tool INTEGER NOT NULL,total INTEGER NOT NULL,cost_usd REAL NOT NULL,pricing_status TEXT NOT NULL,PRIMARY KEY(provider,session_id,model));
INSERT INTO sources VALUES('/old/subagents/agent-a.jsonl','claude','parent',10,10,10,'');
INSERT INTO sessions VALUES('claude','parent','/work',1,2,'ok');
INSERT INTO usage VALUES('claude','parent','model',100,0,0,10,0,0,110,1.0,'priced');`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("legacy merged sessions were retained: %d", count)
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sources') WHERE name IN ('kind','parent_session_id','agent_name','agent_path')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("subagent source migration incomplete: %d columns", count)
	}
}

func TestReportedCostsReplaceCoveredDaysAndPreserveZeroSpendCoverage(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	day := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	cost := core.ReportedCost{
		Provider: core.Claude, Day: day, Model: "claude-opus-5",
		AmountNanoUSD: 1_250_000_000, Source: anthropiccost.Source,
	}
	if err = s.ReplaceReportedCosts(ctx, core.Claude, anthropiccost.Source, []core.ReportedCost{cost}, []time.Time{day}); err != nil {
		t.Fatal(err)
	}
	costs, days, err := s.ReportedCosts(ctx)
	if err != nil || len(costs) != 1 || costs[0].AmountNanoUSD != cost.AmountNanoUSD || len(days) != 1 {
		t.Fatalf("stored ledger = costs %#v days %#v err %v", costs, days, err)
	}
	if err = s.ReplaceReportedCosts(ctx, core.Claude, anthropiccost.Source, nil, []time.Time{day}); err != nil {
		t.Fatal(err)
	}
	costs, days, err = s.ReportedCosts(ctx)
	if err != nil || len(costs) != 0 || len(days) != 1 || !days[0].Equal(day) {
		t.Fatalf("zero-cost replacement lost coverage: costs %#v days %#v err %v", costs, days, err)
	}
}

func TestPricingFingerprintInvalidatesDerivedIndexOnce(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	changed, err := s.EnsurePricingFingerprint("catalog-a")
	if err != nil || changed {
		t.Fatalf("initial empty catalog registration = (%v, %v), want (false, nil)", changed, err)
	}
	_, err = s.db.Exec(`INSERT INTO sources(path,provider,session_id,size,mtime_ns,offset,parser_state,kind,parent_session_id,agent_name,agent_path) VALUES('/source','codex','session',1,1,1,'','session','','','')`)
	if err != nil {
		t.Fatal(err)
	}
	changed, err = s.EnsurePricingFingerprint("catalog-b")
	if err != nil || !changed {
		t.Fatalf("changed catalog = (%v, %v), want (true, nil)", changed, err)
	}
	var sources int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&sources); err != nil || sources != 0 {
		t.Fatalf("stale sources retained: count=%d err=%v", sources, err)
	}
	changed, err = s.EnsurePricingFingerprint("catalog-b")
	if err != nil || changed {
		t.Fatalf("unchanged catalog = (%v, %v), want (false, nil)", changed, err)
	}
}

func testSourceStat(t *testing.T) (string, os.FileInfo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, stat
}

func TestUsageDaysLedgerRecordsIncrementalGrowthPerDay(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	path, stat := testSourceStat(t)
	day1 := time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)
	apply := func(updated time.Time, usage core.Usage) {
		t.Helper()
		parsed := core.Parsed{
			Session: core.Session{
				Provider: core.Claude, ID: "session-1", Project: "/work",
				StartedAt: day1, UpdatedAt: updated,
				Usage: []core.Usage{usage},
			},
			Provider: core.Claude, SourcePath: path,
		}
		if err := s.Apply(ctx, parsed, stat); err != nil {
			t.Fatal(err)
		}
	}
	apply(day1, core.Usage{Model: "model-a", Input: 100, Output: 10, Total: 110, CostUSD: 1.0, PricingStatus: "priced"})
	apply(day2, core.Usage{Model: "model-a", Input: 50, Output: 5, Total: 55, CostUSD: 0.5, PricingStatus: "priced"})

	rows, err := s.UsageDays(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d ledger rows, want one per day: %#v", len(rows), rows)
	}
	if rows[0].Usage.Total != 110 || !rows[0].Day.Equal(time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("day one growth misattributed: %#v", rows[0])
	}
	if rows[1].Usage.Total != 55 || !rows[1].Day.Equal(time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("day two growth misattributed: %#v", rows[1])
	}

	// The since bound trims earlier days.
	rows, err = s.UsageDays(ctx, day2)
	if err != nil || len(rows) != 1 || rows[0].Usage.Total != 55 {
		t.Fatalf("since bound not applied: %#v err %v", rows, err)
	}
}

func TestUsageDaysLedgerDiffsReplaceSnapshots(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	path, stat := testSourceStat(t)
	day1 := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)
	apply := func(updated time.Time, usage ...core.Usage) {
		t.Helper()
		parsed := core.Parsed{
			Session: core.Session{
				Provider: core.OpenCode, ID: "session-1", Project: "/work",
				StartedAt: day1, UpdatedAt: updated, Usage: usage,
			},
			Provider: core.OpenCode, SourcePath: path, Replace: true,
		}
		if err := s.Apply(ctx, parsed, stat); err != nil {
			t.Fatal(err)
		}
	}
	apply(day1,
		core.Usage{Model: "model-a", Input: 100, Total: 100, CostUSD: 1.0, PricingStatus: "priced"},
		core.Usage{Model: "model-b", Input: 40, Total: 40, CostUSD: 0.4, PricingStatus: "priced"})
	// Day two: model-a grew by 50, model-b vanished from the snapshot.
	apply(day2, core.Usage{Model: "model-a", Input: 150, Total: 150, CostUSD: 1.5, PricingStatus: "priced"})

	rows, err := s.UsageDays(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	perDay := map[string]int64{}
	var ledgerTotal int64
	for _, row := range rows {
		perDay[row.Day.Format("2006-01-02")+"/"+row.Usage.Model] += row.Usage.Total
		ledgerTotal += row.Usage.Total
	}
	if perDay["2026-08-10/model-a"] != 100 || perDay["2026-08-10/model-b"] != 40 {
		t.Fatalf("first snapshot not fully attributed to day one: %#v", perDay)
	}
	if perDay["2026-08-11/model-a"] != 50 || perDay["2026-08-11/model-b"] != -40 {
		t.Fatalf("replace deltas wrong: %#v", perDay)
	}
	// The ledger's sum matches the current cumulative snapshot.
	if ledgerTotal != 150 {
		t.Fatalf("ledger sum %d does not match cumulative total 150", ledgerTotal)
	}
}

func TestUsageDaysLedgerFoldsSubagentGrowthIntoParent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	path, stat := testSourceStat(t)
	day := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	parsed := core.Parsed{
		Subagent: &core.Subagent{
			ID: "agent-1", ParentID: "parent-1", StartedAt: day, UpdatedAt: day,
			Usage: []core.Usage{{Model: "model-a", Input: 30, Total: 30, CostUSD: 0.3, PricingStatus: "priced"}},
		},
		Provider: core.Claude, SourcePath: path,
	}
	if err = s.Apply(ctx, parsed, stat); err != nil {
		t.Fatal(err)
	}
	rows, err := s.UsageDays(ctx, time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("subagent ledger rows: %#v err %v", rows, err)
	}
	if rows[0].SessionID != "parent-1" || rows[0].Usage.Total != 30 {
		t.Fatalf("subagent growth not folded into parent: %#v", rows[0])
	}
}

func TestMissingUsageDaysTableTriggersRebuild(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	path, stat := testSourceStat(t)
	parsed := core.Parsed{
		Session: core.Session{
			Provider: core.Claude, ID: "session-1", StartedAt: time.Now(), UpdatedAt: time.Now(),
			Usage: []core.Usage{{Model: "model-a", Input: 10, Total: 10, PricingStatus: "priced"}},
		},
		Provider: core.Claude, SourcePath: path,
	}
	if err = s.Apply(context.Background(), parsed, stat); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DROP TABLE usage_days`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var sessions int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("index without a daily ledger was not rebuilt: sessions=%d err=%v", sessions, err)
	}
}
