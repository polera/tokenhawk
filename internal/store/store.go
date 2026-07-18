package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/polera/tokenhawk/internal/core"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{db: db}
	if err = s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) init() error {
	_, err := s.db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS sources(path TEXT PRIMARY KEY,provider TEXT NOT NULL,session_id TEXT NOT NULL,size INTEGER NOT NULL,mtime_ns INTEGER NOT NULL,offset INTEGER NOT NULL,parser_state TEXT NOT NULL DEFAULT '',kind TEXT NOT NULL DEFAULT 'session',parent_session_id TEXT NOT NULL DEFAULT '',agent_name TEXT NOT NULL DEFAULT '',agent_path TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS sessions(provider TEXT NOT NULL,id TEXT NOT NULL,project TEXT NOT NULL DEFAULT '',started_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,source_health TEXT NOT NULL DEFAULT 'ok',PRIMARY KEY(provider,id));
CREATE TABLE IF NOT EXISTS usage(provider TEXT NOT NULL,session_id TEXT NOT NULL,model TEXT NOT NULL,input INTEGER NOT NULL,cached_input INTEGER NOT NULL,cache_creation INTEGER NOT NULL,output INTEGER NOT NULL,reasoning INTEGER NOT NULL,tool INTEGER NOT NULL,total INTEGER NOT NULL,cost_usd REAL NOT NULL,pricing_status TEXT NOT NULL,PRIMARY KEY(provider,session_id,model));
CREATE TABLE IF NOT EXISTS subagents(provider TEXT NOT NULL,parent_session_id TEXT NOT NULL,id TEXT NOT NULL,name TEXT NOT NULL DEFAULT '',agent_path TEXT NOT NULL DEFAULT '',started_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,status TEXT NOT NULL DEFAULT 'unknown',source_health TEXT NOT NULL DEFAULT 'ok',PRIMARY KEY(provider,parent_session_id,id));
CREATE TABLE IF NOT EXISTS subagent_usage(provider TEXT NOT NULL,parent_session_id TEXT NOT NULL,subagent_id TEXT NOT NULL,model TEXT NOT NULL,input INTEGER NOT NULL,cached_input INTEGER NOT NULL,cache_creation INTEGER NOT NULL,output INTEGER NOT NULL,reasoning INTEGER NOT NULL,tool INTEGER NOT NULL,total INTEGER NOT NULL,cost_usd REAL NOT NULL,pricing_status TEXT NOT NULL,PRIMARY KEY(provider,parent_session_id,subagent_id,model));
CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS sessions_updated ON sessions(updated_at DESC);`)
	if err != nil {
		return err
	}
	// Existing databases predate subagent source metadata. SQLite has no
	// ADD COLUMN IF NOT EXISTS, so inspect first and migrate in place.
	migrated := false
	for name, definition := range map[string]string{
		"kind": "TEXT NOT NULL DEFAULT 'session'", "parent_session_id": "TEXT NOT NULL DEFAULT ''",
		"agent_name": "TEXT NOT NULL DEFAULT ''", "agent_path": "TEXT NOT NULL DEFAULT ''",
	} {
		var added bool
		if added, err = s.ensureSourceColumn(name, definition); err != nil {
			return err
		}
		migrated = migrated || added
	}
	if migrated {
		// The old parser merged Claude/Codex child logs into parent usage. The
		// index is derived data, so a one-time rebuild is the only exact repair.
		return s.Reset()
	}
	return nil
}

// EnsurePricingFingerprint invalidates the derived index when bundled or
// override rates change. Unchanged source files must be reparsed to replace
// their stored estimates, so this makes catalog upgrades self-healing.
func (s *Store) EnsurePricingFingerprint(fingerprint string) (bool, error) {
	var current string
	err := s.db.QueryRow(`SELECT value FROM metadata WHERE key='pricing_fingerprint'`).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if current == fingerprint {
		return false, nil
	}
	var sources int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&sources); err != nil {
		return false, err
	}
	if sources > 0 {
		if err = s.Reset(); err != nil {
			return false, err
		}
	}
	_, err = s.db.Exec(`INSERT INTO metadata(key,value) VALUES('pricing_fingerprint',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, fingerprint)
	return sources > 0, err
}

func (s *Store) ensureSourceColumn(name, definition string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(sources)`)
	if err != nil {
		return false, err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var column, typ string
		var defaultValue any
		if err = rows.Scan(&cid, &column, &typ, &notnull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return false, err
		}
		if column == name {
			found = true
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	_ = rows.Close()
	if found {
		return false, nil
	}
	_, err = s.db.Exec(`ALTER TABLE sources ADD COLUMN ` + name + ` ` + definition)
	return err == nil, err
}

func (s *Store) Source(path string) (core.SourceState, error) {
	var x core.SourceState
	var p string
	err := s.db.QueryRow(`SELECT path,provider,session_id,size,mtime_ns,offset,parser_state,kind,parent_session_id,agent_name,agent_path FROM sources WHERE path=?`, path).Scan(&x.Path, &p, &x.SessionID, &x.Size, &x.ModTimeNS, &x.Offset, &x.ParserState, &x.Kind, &x.ParentID, &x.Name, &x.AgentPath)
	x.Provider = core.Provider(p)
	if err == sql.ErrNoRows {
		return core.SourceState{Path: path}, nil
	}
	return x, err
}

func (s *Store) Apply(ctx context.Context, parsed core.Parsed, stat os.FileInfo) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p := string(parsed.Provider)
	if parsed.Subagent != nil {
		return s.applySubagent(ctx, tx, parsed, stat)
	}
	id := parsed.Session.ID
	if id == "" {
		return fmt.Errorf("empty session id for %s", parsed.Session.SourcePath)
	}
	if parsed.Replace {
		if _, err = tx.ExecContext(ctx, `DELETE FROM usage WHERE provider=? AND session_id=?`, p, id); err != nil {
			return err
		}
	}
	started, updated := parsed.Session.StartedAt.UnixNano(), parsed.Session.UpdatedAt.UnixNano()
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions(provider,id,project,started_at,updated_at,source_health) VALUES(?,?,?,?,?,?)
ON CONFLICT(provider,id) DO UPDATE SET project=CASE WHEN excluded.project<>'' THEN excluded.project ELSE sessions.project END,started_at=CASE WHEN sessions.started_at=0 OR (excluded.started_at>0 AND excluded.started_at<sessions.started_at) THEN excluded.started_at ELSE sessions.started_at END,updated_at=MAX(sessions.updated_at,excluded.updated_at),source_health=excluded.source_health`, p, id, parsed.Session.Project, started, updated, parsed.Session.SourceHealth)
	if err != nil {
		return err
	}
	for _, u := range parsed.Session.Usage {
		if parsed.Replace {
			_, err = tx.ExecContext(ctx, `INSERT INTO usage(provider,session_id,model,input,cached_input,cache_creation,output,reasoning,tool,total,cost_usd,pricing_status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, p, id, u.Model, u.Input, u.CachedInput, u.CacheCreation, u.Output, u.Reasoning, u.Tool, u.Total, u.CostUSD, u.PricingStatus)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO usage(provider,session_id,model,input,cached_input,cache_creation,output,reasoning,tool,total,cost_usd,pricing_status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(provider,session_id,model) DO UPDATE SET input=input+excluded.input,cached_input=cached_input+excluded.cached_input,cache_creation=cache_creation+excluded.cache_creation,output=output+excluded.output,reasoning=reasoning+excluded.reasoning,tool=tool+excluded.tool,total=total+excluded.total,cost_usd=cost_usd+excluded.cost_usd,pricing_status=CASE WHEN usage.pricing_status='reported' AND excluded.pricing_status='reported' THEN 'reported' WHEN usage.pricing_status IN ('priced','reported') AND excluded.pricing_status IN ('priced','reported') THEN 'priced' ELSE 'unpriced' END`, p, id, u.Model, u.Input, u.CachedInput, u.CacheCreation, u.Output, u.Reasoning, u.Tool, u.Total, u.CostUSD, u.PricingStatus)
		}
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sources(path,provider,session_id,size,mtime_ns,offset,parser_state,kind,parent_session_id,agent_name,agent_path) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET provider=excluded.provider,session_id=excluded.session_id,size=excluded.size,mtime_ns=excluded.mtime_ns,offset=excluded.offset,parser_state=excluded.parser_state,kind=excluded.kind,parent_session_id=excluded.parent_session_id,agent_name=excluded.agent_name,agent_path=excluded.agent_path`, parsed.SourcePath, p, id, stat.Size(), stat.ModTime().UnixNano(), parsed.Offset, parsed.ParserState, "session", "", "", "")
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) applySubagent(ctx context.Context, tx *sql.Tx, parsed core.Parsed, stat os.FileInfo) error {
	a := parsed.Subagent
	p, parentID, id := string(parsed.Provider), a.ParentID, a.ID
	if id == "" || parentID == "" {
		return fmt.Errorf("empty subagent or parent id for %s", parsed.SourcePath)
	}
	if parsed.Replace {
		if _, err := tx.ExecContext(ctx, `DELETE FROM subagent_usage WHERE provider=? AND parent_session_id=? AND subagent_id=?`, p, parentID, id); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO subagents(provider,parent_session_id,id,name,agent_path,started_at,updated_at,status,source_health) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(provider,parent_session_id,id) DO UPDATE SET name=CASE WHEN excluded.name<>'' THEN excluded.name ELSE subagents.name END,agent_path=CASE WHEN excluded.agent_path<>'' THEN excluded.agent_path ELSE subagents.agent_path END,started_at=CASE WHEN subagents.started_at=0 OR (excluded.started_at>0 AND excluded.started_at<subagents.started_at) THEN excluded.started_at ELSE subagents.started_at END,updated_at=MAX(subagents.updated_at,excluded.updated_at),status=CASE WHEN excluded.status<>'unknown' THEN excluded.status ELSE subagents.status END,source_health=excluded.source_health`, p, parentID, id, a.Name, a.AgentPath, a.StartedAt.UnixNano(), a.UpdatedAt.UnixNano(), a.Status, a.SourceHealth)
	if err != nil {
		return err
	}
	for _, u := range a.Usage {
		if parsed.Replace {
			_, err = tx.ExecContext(ctx, `INSERT INTO subagent_usage(provider,parent_session_id,subagent_id,model,input,cached_input,cache_creation,output,reasoning,tool,total,cost_usd,pricing_status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, p, parentID, id, u.Model, u.Input, u.CachedInput, u.CacheCreation, u.Output, u.Reasoning, u.Tool, u.Total, u.CostUSD, u.PricingStatus)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO subagent_usage(provider,parent_session_id,subagent_id,model,input,cached_input,cache_creation,output,reasoning,tool,total,cost_usd,pricing_status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(provider,parent_session_id,subagent_id,model) DO UPDATE SET input=input+excluded.input,cached_input=cached_input+excluded.cached_input,cache_creation=cache_creation+excluded.cache_creation,output=output+excluded.output,reasoning=reasoning+excluded.reasoning,tool=tool+excluded.tool,total=total+excluded.total,cost_usd=cost_usd+excluded.cost_usd,pricing_status=CASE WHEN subagent_usage.pricing_status='reported' AND excluded.pricing_status='reported' THEN 'reported' WHEN subagent_usage.pricing_status IN ('priced','reported') AND excluded.pricing_status IN ('priced','reported') THEN 'priced' ELSE 'unpriced' END`, p, parentID, id, u.Model, u.Input, u.CachedInput, u.CacheCreation, u.Output, u.Reasoning, u.Tool, u.Total, u.CostUSD, u.PricingStatus)
		}
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sources(path,provider,session_id,size,mtime_ns,offset,parser_state,kind,parent_session_id,agent_name,agent_path) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET provider=excluded.provider,session_id=excluded.session_id,size=excluded.size,mtime_ns=excluded.mtime_ns,offset=excluded.offset,parser_state=excluded.parser_state,kind=excluded.kind,parent_session_id=excluded.parent_session_id,agent_name=excluded.agent_name,agent_path=excluded.agent_path`, parsed.SourcePath, p, id, stat.Size(), stat.ModTime().UnixNano(), parsed.Offset, parsed.ParserState, "subagent", parentID, a.Name, a.AgentPath)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) List(ctx context.Context, f core.Filter, activeWindow time.Duration, includeSource bool) ([]core.Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider,id,project,started_at,updated_at,source_health FROM sessions ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Session
	now := time.Now()
	for rows.Next() {
		var x core.Session
		var p string
		var started, updated int64
		if err = rows.Scan(&p, &x.ID, &x.Project, &started, &updated, &x.SourceHealth); err != nil {
			return nil, err
		}
		x.Provider = core.Provider(p)
		x.StartedAt = time.Unix(0, started)
		x.UpdatedAt = time.Unix(0, updated)
		x.Active = now.Sub(x.UpdatedAt) <= activeWindow
		if f.Provider != "" && x.Provider != f.Provider || f.Project != "" && !strings.Contains(strings.ToLower(x.Project), strings.ToLower(f.Project)) || !f.Since.IsZero() && x.UpdatedAt.Before(f.Since) || !f.Until.IsZero() && x.UpdatedAt.After(f.Until) {
			continue
		}
		if includeSource {
			_ = s.db.QueryRowContext(ctx, `SELECT path FROM sources WHERE provider=? AND session_id=? AND kind='session' ORDER BY mtime_ns DESC LIMIT 1`, p, x.ID).Scan(&x.SourcePath)
		}
		u, er := s.usage(ctx, x.Provider, x.ID)
		if er != nil {
			return nil, er
		}
		x.Usage = u
		agents, er := s.subagents(ctx, x.Provider, x.ID, activeWindow, now)
		if er != nil {
			return nil, er
		}
		x.Subagents = agents
		if x.RunningSubagents() > 0 {
			x.Active = true
		}
		if f.Status == "active" && !x.Active || f.Status == "inactive" && x.Active {
			continue
		}
		if f.Model != "" {
			found := false
			for _, v := range u {
				if strings.Contains(strings.ToLower(v.Model), strings.ToLower(f.Model)) {
					found = true
					break
				}
			}
			for _, a := range agents {
				for _, v := range a.Usage {
					if strings.Contains(strings.ToLower(v.Model), strings.ToLower(f.Model)) {
						found = true
						break
					}
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) subagents(ctx context.Context, p core.Provider, parentID string, activeWindow time.Duration, now time.Time) ([]core.Subagent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,agent_path,started_at,updated_at,status,source_health FROM subagents WHERE provider=? AND parent_session_id=? ORDER BY updated_at DESC`, string(p), parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Subagent
	for rows.Next() {
		var a core.Subagent
		var started, updated int64
		if err = rows.Scan(&a.ID, &a.Name, &a.AgentPath, &started, &updated, &a.Status, &a.SourceHealth); err != nil {
			return nil, err
		}
		a.ParentID = parentID
		a.StartedAt = time.Unix(0, started)
		a.UpdatedAt = time.Unix(0, updated)
		terminal := a.Status == "completed" || a.Status == "closed" || a.Status == "failed" || a.Status == "cancelled"
		a.Running = !terminal && now.Sub(a.UpdatedAt) <= activeWindow
		if a.Running {
			a.Status = "running"
		} else if a.Status == "unknown" || a.Status == "open" || a.Status == "running" {
			a.Status = "inactive"
		}
		a.Usage, err = s.subagentUsage(ctx, p, parentID, a.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) subagentUsage(ctx context.Context, p core.Provider, parentID, id string) ([]core.Usage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model,input,cached_input,cache_creation,output,reasoning,tool,total,cost_usd,pricing_status FROM subagent_usage WHERE provider=? AND parent_session_id=? AND subagent_id=? ORDER BY model`, string(p), parentID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Usage
	for rows.Next() {
		var u core.Usage
		if err = rows.Scan(&u.Model, &u.Input, &u.CachedInput, &u.CacheCreation, &u.Output, &u.Reasoning, &u.Tool, &u.Total, &u.CostUSD, &u.PricingStatus); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) usage(ctx context.Context, p core.Provider, id string) ([]core.Usage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model,input,cached_input,cache_creation,output,reasoning,tool,total,cost_usd,pricing_status FROM usage WHERE provider=? AND session_id=? ORDER BY model`, string(p), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Usage
	for rows.Next() {
		var u core.Usage
		if err = rows.Scan(&u.Model, &u.Input, &u.CachedInput, &u.CacheCreation, &u.Output, &u.Reasoning, &u.Tool, &u.Total, &u.CostUSD, &u.PricingStatus); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) Reconcile(ctx context.Context, paths []string) error {
	return s.reconcile(ctx, paths, "")
}

// ReconcileProvider removes stale sources for one provider without disturbing
// sessions maintained by other provider-specific status-line refreshes.
func (s *Store) ReconcileProvider(ctx context.Context, paths []string, provider core.Provider) error {
	return s.reconcile(ctx, paths, provider)
}

func (s *Store) reconcile(ctx context.Context, paths []string, provider core.Provider) error {
	keep := map[string]bool{}
	for _, p := range paths {
		keep[p] = true
	}
	query := `SELECT path FROM sources`
	args := []any{}
	if provider != "" {
		query += ` WHERE provider=?`
		args = append(args, string(provider))
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		if !keep[p] {
			gone = append(gone, p)
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, p := range gone {
		if _, err = s.db.ExecContext(ctx, `DELETE FROM sources WHERE path=?`, p); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM subagents WHERE NOT EXISTS(SELECT 1 FROM sources WHERE sources.kind='subagent' AND sources.provider=subagents.provider AND sources.parent_session_id=subagents.parent_session_id AND sources.session_id=subagents.id);
DELETE FROM subagent_usage WHERE NOT EXISTS(SELECT 1 FROM subagents WHERE subagents.provider=subagent_usage.provider AND subagents.parent_session_id=subagent_usage.parent_session_id AND subagents.id=subagent_usage.subagent_id);
DELETE FROM sessions WHERE NOT EXISTS(SELECT 1 FROM sources WHERE sources.kind='session' AND sources.provider=sessions.provider AND sources.session_id=sessions.id);
DELETE FROM usage WHERE NOT EXISTS(SELECT 1 FROM sessions WHERE sessions.provider=usage.provider AND sessions.id=usage.session_id);
DELETE FROM subagents WHERE NOT EXISTS(SELECT 1 FROM sessions WHERE sessions.provider=subagents.provider AND sessions.id=subagents.parent_session_id);
DELETE FROM subagent_usage WHERE NOT EXISTS(SELECT 1 FROM subagents WHERE subagents.provider=subagent_usage.provider AND subagents.parent_session_id=subagent_usage.parent_session_id AND subagents.id=subagent_usage.subagent_id)`)
	return err
}

func (s *Store) Reset() error {
	_, err := s.db.Exec(`DELETE FROM sources;DELETE FROM subagent_usage;DELETE FROM subagents;DELETE FROM usage;DELETE FROM sessions;`)
	return err
}
func SortByCost(sessions []core.Session) {
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].Totals().CostUSD > sessions[j].Totals().CostUSD })
}
