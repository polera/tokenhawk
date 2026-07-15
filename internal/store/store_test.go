package store

import (
	"database/sql"
	"path/filepath"
	"testing"
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
