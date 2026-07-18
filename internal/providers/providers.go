package providers

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/polera/tokenhawk/internal/core"
	_ "modernc.org/sqlite"
)

func Discover(claudeDir, codexDir, geminiDir, piDir, openCodeDB string) ([]string, error) {
	var paths []string
	roots := []struct {
		root  string
		match func(string) bool
	}{
		{claudeDir, func(p string) bool { return strings.HasSuffix(p, ".jsonl") }},
		{filepath.Join(codexDir, "sessions"), func(p string) bool { return strings.HasSuffix(p, ".jsonl") }},
		{filepath.Join(codexDir, "archived_sessions"), func(p string) bool { return strings.HasSuffix(p, ".jsonl") }},
		{geminiDir, func(p string) bool {
			return strings.HasSuffix(p, ".json") && strings.HasPrefix(filepath.Base(p), "session-") && filepath.Base(filepath.Dir(p)) == "chats"
		}},
		{piDir, func(p string) bool { return strings.HasSuffix(p, ".jsonl") }},
	}
	for _, r := range roots {
		if r.root == "" {
			continue
		}
		err := filepath.WalkDir(r.root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
					return nil
				}
				return err
			}
			if !d.IsDir() && r.match(path) {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if openCodeDB == "" {
		return paths, nil
	}
	if stat, err := os.Stat(openCodeDB); err == nil && !stat.IsDir() {
		paths = append(paths, openCodeDB)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
		return nil, err
	}
	return paths, nil
}

func ProviderFor(path, claudeDir, codexDir, geminiDir, piDir, openCodeDB string) core.Provider {
	clean := filepath.Clean(path)
	if strings.HasPrefix(clean, filepath.Clean(openCodeDB)+"#") || clean == filepath.Clean(openCodeDB) {
		return core.OpenCode
	}
	if within(clean, filepath.Clean(claudeDir)) {
		return core.Claude
	}
	if within(clean, filepath.Clean(codexDir)) {
		return core.Codex
	}
	if within(clean, filepath.Clean(geminiDir)) {
		return core.Gemini
	}
	if within(clean, filepath.Clean(piDir)) {
		return core.Pi
	}
	return ""
}

func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func Parse(path string, provider core.Provider, previous core.SourceState) (core.Parsed, error) {
	switch provider {
	case core.Claude:
		return parseClaude(path, previous)
	case core.Codex:
		return parseCodex(path, previous)
	case core.Gemini:
		return parseGemini(path)
	case core.Pi:
		return parsePi(path, previous)
	default:
		return core.Parsed{}, fmt.Errorf("unknown provider for %s", path)
	}
}

type piRecord struct {
	Type      string    `json:"type"`
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	CWD       string    `json:"cwd"`
	Message   struct {
		Role     string `json:"role"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Usage    struct {
			Input       int64 `json:"input"`
			Output      int64 `json:"output"`
			CacheRead   int64 `json:"cacheRead"`
			CacheWrite  int64 `json:"cacheWrite"`
			TotalTokens int64 `json:"totalTokens"`
			Cost        struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
	} `json:"message"`
}

func parsePi(path string, previous core.SourceState) (core.Parsed, error) {
	session := core.Session{Provider: core.Pi, ID: previous.SessionID, SourcePath: path, SourceHealth: "ok"}
	usage := map[string]*core.Usage{}
	offset, err := scanJSONL(path, previous.Offset, func(line []byte) error {
		var record piRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return err
		}
		times(&session, record.Timestamp)
		if record.Type == "session" {
			if record.ID != "" {
				session.ID = record.ID
			}
			if record.CWD != "" {
				session.Project = record.CWD
			}
		}
		if record.Type != "message" || record.Message.Role != "assistant" || record.Message.Model == "" {
			return nil
		}
		model := record.Message.Model
		if record.Message.Provider != "" {
			model = record.Message.Provider + "/" + model
		}
		u := get(usage, model)
		u.Input += record.Message.Usage.Input + record.Message.Usage.CacheRead
		u.CachedInput += record.Message.Usage.CacheRead
		u.CacheCreation += record.Message.Usage.CacheWrite
		u.Output += record.Message.Usage.Output
		if record.Message.Usage.TotalTokens > 0 {
			u.Total += record.Message.Usage.TotalTokens
		} else {
			u.Total += record.Message.Usage.Input + record.Message.Usage.CacheRead + record.Message.Usage.CacheWrite + record.Message.Usage.Output
		}
		u.CostUSD += record.Message.Usage.Cost.Total
		u.PricingStatus = "reported"
		return nil
	})
	if err != nil {
		return core.Parsed{}, err
	}
	finalize(path, &session, usage)
	return core.Parsed{Session: session, Provider: core.Pi, SourcePath: path, Offset: offset, Replace: previous.Offset == 0}, nil
}

type openCodeSessionRow struct {
	id, directory, parentID, title, agent string
	created, updated                      int64
}

type openCodeMessage struct {
	Role       string  `json:"role"`
	ModelID    string  `json:"modelID"`
	ProviderID string  `json:"providerID"`
	Cost       float64 `json:"cost"`
	Tokens     struct {
		Input     int64 `json:"input"`
		Output    int64 `json:"output"`
		Reasoning int64 `json:"reasoning"`
		Total     int64 `json:"total"`
		Cache     struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
}

// ParseOpenCodeDB reads OpenCode's SQLite store without modifying it. Each
// returned record uses a stable synthetic source path so Tokenhawk can
// reconcile sessions independently even though they share one database file.
// The optional unchanged callback avoids rereading message rows for sessions
// whose OpenCode update timestamp is already indexed. sources always contains
// every current synthetic path so callers can still reconcile deletions.
func ParseOpenCodeDB(path string, unchanged func(source string, updated time.Time) bool) ([]core.Parsed, []string, error) {
	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", databaseURL.String()+"?mode=ro&_pragma=busy_timeout%3D1000")
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id,directory,parent_id,title,agent,time_created,time_updated FROM session ORDER BY time_updated`)
	if err != nil {
		return nil, nil, fmt.Errorf("read OpenCode sessions: %w", err)
	}
	var sessions []openCodeSessionRow
	for rows.Next() {
		var row openCodeSessionRow
		var parent, title, agent sql.NullString
		if err = rows.Scan(&row.id, &row.directory, &parent, &title, &agent, &row.created, &row.updated); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		row.parentID, row.title, row.agent = parent.String, title.String, agent.String
		sessions = append(sessions, row)
	}
	if err = rows.Close(); err != nil {
		return nil, nil, err
	}
	stat, _ := os.Stat(path)
	var parsed []core.Parsed
	var sources []string
	for _, row := range sessions {
		started, updated := time.UnixMilli(row.created), time.UnixMilli(row.updated)
		if row.created == 0 && stat != nil {
			started = stat.ModTime()
		}
		if row.updated == 0 && stat != nil {
			updated = stat.ModTime()
		}
		source := path + "#session=" + row.id
		sources = append(sources, source)
		if unchanged != nil && unchanged(source, updated) {
			continue
		}
		usage, err := openCodeUsage(db, row.id)
		if err != nil {
			return nil, nil, err
		}
		if row.parentID != "" {
			name := row.title
			if name == "" {
				name = row.agent
			}
			agent := core.Subagent{ID: row.id, ParentID: row.parentID, Name: name, AgentPath: row.agent, StartedAt: started, UpdatedAt: updated, Status: "unknown", SourceHealth: "ok", Usage: usage}
			parsed = append(parsed, core.Parsed{Subagent: &agent, Provider: core.OpenCode, SourcePath: source, Offset: updated.UnixNano(), Replace: true})
			continue
		}
		session := core.Session{Provider: core.OpenCode, ID: row.id, Project: row.directory, StartedAt: started, UpdatedAt: updated, SourcePath: source, SourceHealth: "ok", Usage: usage}
		parsed = append(parsed, core.Parsed{Session: session, Provider: core.OpenCode, SourcePath: source, Offset: updated.UnixNano(), Replace: true})
	}
	return parsed, sources, nil
}

func openCodeUsage(db *sql.DB, sessionID string) ([]core.Usage, error) {
	rows, err := db.Query(`SELECT data FROM message WHERE session_id=? ORDER BY time_created,id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode messages for %s: %w", sessionID, err)
	}
	defer rows.Close()
	usage := map[string]*core.Usage{}
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var message openCodeMessage
		if json.Unmarshal([]byte(data), &message) != nil || message.Role != "assistant" {
			continue
		}
		model := message.ModelID
		if model == "" {
			model = "unknown"
		}
		if message.ProviderID != "" {
			model = message.ProviderID + "/" + model
		}
		u := get(usage, model)
		u.Input += message.Tokens.Input + message.Tokens.Cache.Read
		u.CachedInput += message.Tokens.Cache.Read
		u.CacheCreation += message.Tokens.Cache.Write
		u.Output += message.Tokens.Output
		u.Reasoning += message.Tokens.Reasoning
		if message.Tokens.Total > 0 {
			u.Total += message.Tokens.Total
		} else {
			u.Total += message.Tokens.Input + message.Tokens.Cache.Read + message.Tokens.Cache.Write + message.Tokens.Output + message.Tokens.Reasoning
		}
		u.CostUSD += message.Cost
		u.PricingStatus = "reported"
	}
	var out []core.Usage
	for _, item := range usage {
		if hasUsage(*item) || item.CostUSD != 0 {
			out = append(out, *item)
		}
	}
	return out, rows.Err()
}

type claudeRecord struct {
	Type      string    `json:"type"`
	SessionID string    `json:"sessionId"`
	AgentID   string    `json:"agentId"`
	CWD       string    `json:"cwd"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		Model string `json:"model"`
		Usage struct {
			Input       int64 `json:"input_tokens"`
			CacheRead   int64 `json:"cache_read_input_tokens"`
			CacheCreate int64 `json:"cache_creation_input_tokens"`
			Output      int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func parseClaude(path string, previous core.SourceState) (core.Parsed, error) {
	if strings.Contains(filepath.ToSlash(path), "/subagents/") || previous.Kind == "subagent" {
		return parseClaudeSubagent(path, previous)
	}
	s := core.Session{Provider: core.Claude, ID: previous.SessionID, SourcePath: path, SourceHealth: "ok"}
	usage := map[string]*core.Usage{}
	offset, err := scanJSONL(path, previous.Offset, func(line []byte) error {
		var r claudeRecord
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		if r.SessionID != "" {
			s.ID = r.SessionID
		}
		if r.CWD != "" {
			s.Project = r.CWD
		}
		times(&s, r.Timestamp)
		if r.Message.Model != "" && (r.Type == "assistant" || r.Message.Usage.Input+r.Message.Usage.Output+r.Message.Usage.CacheRead+r.Message.Usage.CacheCreate > 0) {
			u := get(usage, r.Message.Model)
			u.Input += r.Message.Usage.Input + r.Message.Usage.CacheRead
			u.CachedInput += r.Message.Usage.CacheRead
			u.CacheCreation += r.Message.Usage.CacheCreate
			u.Output += r.Message.Usage.Output
			u.Total += r.Message.Usage.Input + r.Message.Usage.CacheRead + r.Message.Usage.CacheCreate + r.Message.Usage.Output
		}
		return nil
	})
	if err != nil {
		return core.Parsed{}, err
	}
	finalize(path, &s, usage)
	return core.Parsed{Session: s, Provider: core.Claude, SourcePath: path, Offset: offset, Replace: previous.Offset == 0}, nil
}

func parseClaudeSubagent(path string, previous core.SourceState) (core.Parsed, error) {
	a := core.Subagent{ID: previous.SessionID, ParentID: previous.ParentID, Name: previous.Name, AgentPath: previous.AgentPath, SourceHealth: "ok", Status: "unknown"}
	usage := map[string]*core.Usage{}
	offset, err := scanJSONL(path, previous.Offset, func(line []byte) error {
		var r claudeRecord
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		if r.AgentID != "" {
			a.ID = r.AgentID
		}
		if r.SessionID != "" {
			a.ParentID = r.SessionID
		}
		timesSubagent(&a, r.Timestamp)
		if r.Message.Model != "" && (r.Type == "assistant" || r.Message.Usage.Input+r.Message.Usage.Output+r.Message.Usage.CacheRead+r.Message.Usage.CacheCreate > 0) {
			u := get(usage, r.Message.Model)
			u.Input += r.Message.Usage.Input + r.Message.Usage.CacheRead
			u.CachedInput += r.Message.Usage.CacheRead
			u.CacheCreation += r.Message.Usage.CacheCreate
			u.Output += r.Message.Usage.Output
			u.Total += r.Message.Usage.Input + r.Message.Usage.CacheRead + r.Message.Usage.CacheCreate + r.Message.Usage.Output
		}
		return nil
	})
	if err != nil {
		return core.Parsed{}, err
	}
	if a.ID == "" {
		a.ID = strings.TrimPrefix(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), "agent-")
	}
	if a.ParentID == "" {
		a.ParentID = filepath.Base(filepath.Dir(filepath.Dir(path)))
	}
	if a.Name == "" {
		metaPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".meta.json"
		var meta struct {
			AgentType string `json:"agentType"`
		}
		// #nosec G304 -- metaPath is derived from a session file we already discovered by scanning.
		if b, e := os.ReadFile(metaPath); e == nil && json.Unmarshal(b, &meta) == nil {
			a.Name = meta.AgentType
		}
	}
	finalizeSubagent(path, &a, usage)
	return core.Parsed{Subagent: &a, Provider: core.Claude, SourcePath: path, Offset: offset, Replace: previous.Offset == 0}, nil
}

type codexState struct {
	Model                                   string `json:"model"`
	Input, Cached, Output, Reasoning, Total int64
}

func parseCodex(path string, previous core.SourceState) (core.Parsed, error) {
	state := codexState{}
	_ = json.Unmarshal([]byte(previous.ParserState), &state)
	s := core.Session{Provider: core.Codex, ID: previous.SessionID, SourcePath: path, SourceHealth: "ok"}
	isSubagent := previous.Kind == "subagent"
	parentID, agentName, agentPath := previous.ParentID, previous.Name, previous.AgentPath
	usage := map[string]*core.Usage{}
	offset, err := scanJSONL(path, previous.Offset, func(line []byte) error {
		var row struct {
			Type      string          `json:"type"`
			Timestamp time.Time       `json:"timestamp"`
			Payload   json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		times(&s, row.Timestamp)
		switch row.Type {
		case "session_meta":
			var p struct {
				ID             string          `json:"id"`
				SessionID      string          `json:"session_id"`
				ParentThreadID string          `json:"parent_thread_id"`
				ThreadSource   string          `json:"thread_source"`
				AgentNickname  string          `json:"agent_nickname"`
				AgentPath      string          `json:"agent_path"`
				CWD            string          `json:"cwd"`
				Timestamp      time.Time       `json:"timestamp"`
				Source         json.RawMessage `json:"source"`
			}
			_ = json.Unmarshal(row.Payload, &p)
			var source struct {
				Subagent *struct {
					ThreadSpawn struct {
						ParentThreadID string `json:"parent_thread_id"`
						AgentNickname  string `json:"agent_nickname"`
						AgentPath      string `json:"agent_path"`
					} `json:"thread_spawn"`
				} `json:"subagent"`
			}
			_ = json.Unmarshal(p.Source, &source)
			if p.ThreadSource == "subagent" || p.ParentThreadID != "" || source.Subagent != nil {
				isSubagent = true
				parentID = p.ParentThreadID
				if parentID == "" && source.Subagent != nil {
					parentID = source.Subagent.ThreadSpawn.ParentThreadID
				}
				if parentID == "" {
					parentID = p.SessionID
				}
				agentName, agentPath = p.AgentNickname, p.AgentPath
				if source.Subagent != nil {
					if agentName == "" {
						agentName = source.Subagent.ThreadSpawn.AgentNickname
					}
					if agentPath == "" {
						agentPath = source.Subagent.ThreadSpawn.AgentPath
					}
				}
				s.ID = p.ID
			} else if p.SessionID != "" {
				s.ID = p.SessionID
			} else if p.ID != "" {
				s.ID = p.ID
			}
			s.Project = p.CWD
			times(&s, p.Timestamp)
		case "turn_context":
			var p struct {
				Model string `json:"model"`
				CWD   string `json:"cwd"`
			}
			_ = json.Unmarshal(row.Payload, &p)
			if p.Model != "" {
				state.Model = p.Model
			}
			if p.CWD != "" {
				s.Project = p.CWD
			}
		case "event_msg":
			var p struct {
				Type string `json:"type"`
				Info *struct {
					Total struct {
						Input     int64 `json:"input_tokens"`
						Cached    int64 `json:"cached_input_tokens"`
						Output    int64 `json:"output_tokens"`
						Reasoning int64 `json:"reasoning_output_tokens"`
						Total     int64 `json:"total_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			}
			_ = json.Unmarshal(row.Payload, &p)
			if p.Type != "token_count" || p.Info == nil {
				break
			}
			cur := p.Info.Total
			model := state.Model
			if model == "" {
				model = "unknown"
			}
			u := get(usage, model)
			u.Input += nonnegative(cur.Input - state.Input)
			u.CachedInput += nonnegative(cur.Cached - state.Cached)
			u.Output += nonnegative(cur.Output - state.Output)
			u.Reasoning += nonnegative(cur.Reasoning - state.Reasoning)
			u.Total += nonnegative(cur.Total - state.Total)
			state.Input, state.Cached, state.Output, state.Reasoning, state.Total = cur.Input, cur.Cached, cur.Output, cur.Reasoning, cur.Total
		}
		return nil
	})
	if err != nil {
		return core.Parsed{}, err
	}
	b, _ := json.Marshal(state)
	finalize(path, &s, usage)
	if isSubagent {
		a := core.Subagent{ID: s.ID, ParentID: parentID, Name: agentName, AgentPath: agentPath, StartedAt: s.StartedAt, UpdatedAt: s.UpdatedAt, Status: "unknown", SourceHealth: s.SourceHealth, Usage: s.Usage}
		return core.Parsed{Subagent: &a, Provider: core.Codex, SourcePath: path, Offset: offset, ParserState: string(b), Replace: previous.Offset == 0}, nil
	}
	return core.Parsed{Session: s, Provider: core.Codex, SourcePath: path, Offset: offset, ParserState: string(b), Replace: previous.Offset == 0}, nil
}

type geminiFile struct {
	SessionID string    `json:"sessionId"`
	Start     time.Time `json:"startTime"`
	Updated   time.Time `json:"lastUpdated"`
	Messages  []struct {
		Model  string                                                        `json:"model"`
		Tokens *struct{ Input, Cached, Output, Thoughts, Tool, Total int64 } `json:"tokens"`
	} `json:"messages"`
}

func parseGemini(path string) (core.Parsed, error) {
	// #nosec G304 -- path comes from scanning the provider's own session directory.
	b, err := os.ReadFile(path)
	if err != nil {
		return core.Parsed{}, err
	}
	var f geminiFile
	if err = json.Unmarshal(b, &f); err != nil {
		return core.Parsed{}, err
	}
	s := core.Session{Provider: core.Gemini, ID: f.SessionID, StartedAt: f.Start, UpdatedAt: f.Updated, SourcePath: path, SourceHealth: "ok"}
	usage := map[string]*core.Usage{}
	rootFile := filepath.Join(filepath.Dir(filepath.Dir(path)), ".project_root")
	// #nosec G304 -- rootFile sits alongside the session file we are already reading.
	if p, e := os.ReadFile(rootFile); e == nil {
		s.Project = strings.TrimSpace(string(p))
	}
	for _, m := range f.Messages {
		if m.Tokens == nil {
			continue
		}
		model := m.Model
		if model == "" {
			model = "unknown"
		}
		u := get(usage, model)
		u.Input += m.Tokens.Input
		u.CachedInput += m.Tokens.Cached
		u.Output += m.Tokens.Output
		u.Reasoning += m.Tokens.Thoughts
		u.Tool += m.Tokens.Tool
		if m.Tokens.Total > 0 {
			u.Total += m.Tokens.Total
		} else {
			u.Total += m.Tokens.Input + m.Tokens.Output + m.Tokens.Thoughts + m.Tokens.Tool
		}
	}
	finalize(path, &s, usage)
	return core.Parsed{Session: s, Provider: core.Gemini, SourcePath: path, Offset: int64(len(b)), Replace: true}, nil
}

func scanJSONL(path string, start int64, fn func([]byte) error) (int64, error) {
	// #nosec G304 -- path comes from scanning the provider's own session directory.
	f, err := os.Open(path)
	if err != nil {
		return start, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return start, err
	}
	if start > info.Size() {
		start = 0
	}
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return start, err
	}
	r := bufio.NewReaderSize(f, 64*1024)
	offset := start
	for {
		line, e := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			candidate := bytes.TrimSpace(line)
			if e == nil || json.Valid(candidate) {
				_ = fn(candidate) // A malformed complete record is isolated; later records remain usable.
				offset += int64(len(line))
			}
		}
		if e != nil {
			if errors.Is(e, io.EOF) {
				return offset, nil
			}
			return offset, e
		}
	}
}

func get(m map[string]*core.Usage, model string) *core.Usage {
	u := m[model]
	if u == nil {
		u = &core.Usage{Model: model}
		m[model] = u
	}
	return u
}
func times(s *core.Session, t time.Time) {
	if t.IsZero() {
		return
	}
	if s.StartedAt.IsZero() || t.Before(s.StartedAt) {
		s.StartedAt = t
	}
	if t.After(s.UpdatedAt) {
		s.UpdatedAt = t
	}
}
func timesSubagent(a *core.Subagent, t time.Time) {
	if t.IsZero() {
		return
	}
	if a.StartedAt.IsZero() || t.Before(a.StartedAt) {
		a.StartedAt = t
	}
	if t.After(a.UpdatedAt) {
		a.UpdatedAt = t
	}
}
func finalize(path string, s *core.Session, m map[string]*core.Usage) {
	if st, e := os.Stat(path); e == nil {
		if s.UpdatedAt.IsZero() || st.ModTime().After(s.UpdatedAt) {
			s.UpdatedAt = st.ModTime()
		}
		if s.StartedAt.IsZero() {
			s.StartedAt = st.ModTime()
		}
	}
	if s.ID == "" {
		s.ID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	for _, u := range m {
		if hasUsage(*u) {
			s.Usage = append(s.Usage, *u)
		}
	}
}
func finalizeSubagent(path string, a *core.Subagent, m map[string]*core.Usage) {
	if st, e := os.Stat(path); e == nil {
		if a.UpdatedAt.IsZero() || st.ModTime().After(a.UpdatedAt) {
			a.UpdatedAt = st.ModTime()
		}
		if a.StartedAt.IsZero() {
			a.StartedAt = st.ModTime()
		}
	}
	for _, u := range m {
		if hasUsage(*u) {
			a.Usage = append(a.Usage, *u)
		}
	}
}
func hasUsage(u core.Usage) bool {
	return u.Input != 0 || u.CachedInput != 0 || u.CacheCreation != 0 || u.Output != 0 || u.Reasoning != 0 || u.Tool != 0 || u.Total != 0
}
func nonnegative(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}
