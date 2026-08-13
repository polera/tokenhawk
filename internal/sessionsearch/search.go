// Package sessionsearch searches provider transcripts without adding their
// contents to Tokenhawk's persistent index.
package sessionsearch

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/polera/tokenhawk/internal/config"
	"github.com/polera/tokenhawk/internal/core"
	"github.com/polera/tokenhawk/internal/providers"
	_ "modernc.org/sqlite"
)

const defaultLimit = 100

type Query struct {
	Text          string
	Provider      core.Provider
	SessionID     string
	Project       string
	Role          string
	Since         time.Time
	Until         time.Time
	Limit         int
	CaseSensitive bool
	FullText      bool
}

type Match struct {
	Provider   core.Provider `json:"provider"`
	SessionID  string        `json:"session_id"`
	SubagentID string        `json:"subagent_id,omitempty"`
	Project    string        `json:"project,omitempty"`
	Timestamp  time.Time     `json:"timestamp,omitempty"`
	Role       string        `json:"role"`
	Snippet    string        `json:"snippet"`
}

type Report struct {
	Query       string          `json:"query"`
	Matches     []Match         `json:"matches"`
	Unsupported []core.Provider `json:"unsupported_providers,omitempty"`
	Warnings    []string        `json:"warnings,omitempty"`
}

// Search reads the provider-owned transcript stores directly. Only user and
// assistant text is considered; tool calls, tool results, reasoning, and other
// structured payloads are deliberately excluded.
func Search(ctx context.Context, cfg config.Config, query Query) (Report, error) {
	return search(ctx, cfg, query, false)
}

// Prompts returns every user-authored message belonging to a session, oldest
// first. Full prompt text is held only in the returned in-memory report.
func Prompts(ctx context.Context, cfg config.Config, provider core.Provider, sessionID string) (Report, error) {
	return sessionMessages(ctx, cfg, provider, sessionID, "user")
}

// Conversation returns the user and assistant messages belonging to a session
// in conversational order. Tool calls, results, and reasoning remain excluded.
func Conversation(ctx context.Context, cfg config.Config, provider core.Provider, sessionID string) (Report, error) {
	report, err := sessionMessages(ctx, cfg, provider, sessionID, "")
	if err != nil {
		return Report{}, err
	}
	report.Matches = mergeConversationMatches(report.Matches)
	return report, nil
}

func sessionMessages(ctx context.Context, cfg config.Config, provider core.Provider, sessionID, role string) (Report, error) {
	if provider == "" || sessionID == "" {
		return Report{}, errors.New("provider and session ID are required")
	}
	report, err := search(ctx, cfg, Query{
		Provider: provider, SessionID: sessionID, Role: role, Limit: int(^uint(0) >> 1), FullText: true,
	}, true)
	if err != nil {
		return Report{}, err
	}
	sort.SliceStable(report.Matches, func(i, j int) bool {
		return report.Matches[i].Timestamp.Before(report.Matches[j].Timestamp)
	})
	return report, nil
}

func mergeConversationMatches(matches []Match) []Match {
	merged := make([]Match, 0, len(matches))
	for _, match := range matches {
		if len(merged) == 0 {
			merged = append(merged, match)
			continue
		}
		previous := &merged[len(merged)-1]
		sameMessage := previous.Provider == match.Provider &&
			previous.SessionID == match.SessionID &&
			previous.SubagentID == match.SubagentID &&
			previous.Role == match.Role &&
			previous.Timestamp.Equal(match.Timestamp)
		if !sameMessage {
			merged = append(merged, match)
			continue
		}
		if text := strings.TrimSpace(match.Snippet); text != "" {
			previous.Snippet = strings.TrimSpace(previous.Snippet) + "\n\n" + text
		}
	}
	return merged
}

func search(ctx context.Context, cfg config.Config, query Query, allowEmpty bool) (Report, error) {
	query.Text = strings.TrimSpace(query.Text)
	if query.Text == "" && !allowEmpty {
		return Report{}, errors.New("search query cannot be empty")
	}
	if query.Limit < 0 {
		return Report{}, errors.New("search limit cannot be negative")
	}
	if query.Limit == 0 {
		query.Limit = defaultLimit
	}
	if query.Role != "" && query.Role != "user" && query.Role != "assistant" {
		return Report{}, fmt.Errorf("unsupported role %q (expected user or assistant)", query.Role)
	}
	if query.Provider == core.Agy {
		report := Report{Query: query.Text, Matches: []Match{}}
		if allowEmpty {
			report.Unsupported = []core.Provider{core.Agy}
		}
		return report, nil
	}

	claudeDir, codexDir, geminiDir := cfg.ClaudeDir, cfg.CodexDir, cfg.GeminiDir
	agyDir, piDir, openCodeDB := cfg.AgyDir, cfg.PiDir, cfg.OpenCodeDB
	if query.Provider != "" {
		claudeDir, codexDir, geminiDir, agyDir, piDir, openCodeDB = "", "", "", "", "", ""
		switch query.Provider {
		case core.Claude:
			claudeDir = cfg.ClaudeDir
		case core.Codex:
			codexDir = cfg.CodexDir
		case core.Gemini:
			geminiDir = cfg.GeminiDir
		case core.Pi:
			piDir = cfg.PiDir
		case core.OpenCode:
			openCodeDB = cfg.OpenCodeDB
		}
	}
	paths, err := providers.Discover(claudeDir, codexDir, geminiDir, agyDir, piDir, openCodeDB)
	if err != nil {
		return Report{}, err
	}
	report := Report{Query: query.Text, Matches: []Match{}}
	for _, path := range paths {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		provider := providers.ProviderFor(path, claudeDir, codexDir, geminiDir, agyDir, piDir, openCodeDB)
		if query.Provider != "" && provider != query.Provider {
			continue
		}
		if provider == core.Agy {
			continue
		}

		var matches []Match
		switch provider {
		case core.Claude:
			matches, err = searchClaude(path, query)
		case core.Codex:
			matches, err = searchCodex(path, query)
		case core.Gemini:
			matches, err = searchGemini(path, query)
		case core.Pi:
			matches, err = searchPi(path, query)
		case core.OpenCode:
			matches, err = searchOpenCode(path, query)
		}
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %v", filepath.Base(path), err))
			continue
		}
		report.Matches = append(report.Matches, matches...)
	}
	report.Matches = uniqueMatches(report.Matches)
	sort.SliceStable(report.Matches, func(i, j int) bool {
		return report.Matches[i].Timestamp.After(report.Matches[j].Timestamp)
	})
	if !allowEmpty {
		report.Matches = uniqueSessionMatches(report.Matches)
	}
	if len(report.Matches) > query.Limit {
		report.Matches = report.Matches[:query.Limit]
	}
	return report, nil
}

// uniqueSessionMatches keeps the newest hit for each provider session. Search
// results are review entry points rather than an occurrence list; conversation
// loading bypasses this collapse so session detail still receives every
// message.
func uniqueSessionMatches(matches []Match) []Match {
	seen := map[string]bool{}
	out := matches[:0]
	for _, match := range matches {
		if match.SessionID == "" {
			out = append(out, match)
			continue
		}
		key := string(match.Provider) + "\x00" + match.SessionID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, match)
	}
	return out
}

func uniqueMatches(matches []Match) []Match {
	seen := map[string]bool{}
	out := matches[:0]
	for _, match := range matches {
		key := strings.Join([]string{
			string(match.Provider), match.SessionID, match.SubagentID,
			match.Timestamp.Format(time.RFC3339Nano), match.Role, match.Snippet,
		}, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, match)
	}
	return out
}

type transcriptMessage struct {
	Provider   core.Provider
	SessionID  string
	SubagentID string
	Project    string
	Timestamp  time.Time
	Role       string
	Text       string
}

func matchMessage(query Query, message transcriptMessage) (Match, bool) {
	if query.SessionID != "" && message.SessionID != query.SessionID && message.SubagentID != query.SessionID {
		return Match{}, false
	}
	if query.Role != "" && message.Role != query.Role {
		return Match{}, false
	}
	if query.Project != "" && !contains(message.Project, query.Project, false) {
		return Match{}, false
	}
	if !query.Since.IsZero() && !message.Timestamp.IsZero() && message.Timestamp.Before(query.Since) {
		return Match{}, false
	}
	if !query.Until.IsZero() && !message.Timestamp.IsZero() && message.Timestamp.After(query.Until) {
		return Match{}, false
	}
	if !contains(message.Text, query.Text, query.CaseSensitive) {
		return Match{}, false
	}
	resultText := snippet(message.Text, query.Text, query.CaseSensitive)
	if query.FullText {
		resultText = strings.TrimSpace(message.Text)
	}
	return Match{
		Provider: message.Provider, SessionID: message.SessionID, SubagentID: message.SubagentID,
		Project: message.Project, Timestamp: message.Timestamp, Role: message.Role,
		Snippet: resultText,
	}, true
}

func contains(text, query string, caseSensitive bool) bool {
	if !caseSensitive {
		text, query = strings.ToLower(text), strings.ToLower(query)
	}
	return strings.Contains(text, query)
}

func searchClaude(path string, query Query) ([]Match, error) {
	var matches []Match
	state := transcriptMessage{Provider: core.Claude, SubagentID: claudeSubagentID(path)}
	err := scanJSONL(path, func(line []byte) {
		var row struct {
			Type      string          `json:"type"`
			SessionID string          `json:"sessionId"`
			AgentID   string          `json:"agentId"`
			CWD       string          `json:"cwd"`
			Timestamp time.Time       `json:"timestamp"`
			Message   json.RawMessage `json:"message"`
		}
		if json.Unmarshal(line, &row) != nil {
			return
		}
		if row.SessionID != "" {
			state.SessionID = row.SessionID
		}
		if row.AgentID != "" {
			state.SubagentID = row.AgentID
		}
		if row.CWD != "" {
			state.Project = row.CWD
		}
		role := normalizedRole(row.Type)
		if role == "" {
			return
		}
		var message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(row.Message, &message) != nil {
			return
		}
		if normalized := normalizedRole(message.Role); normalized != "" {
			role = normalized
		}
		for _, text := range textContent(message.Content) {
			candidate := state
			candidate.Timestamp, candidate.Role, candidate.Text = row.Timestamp, role, text
			if match, ok := matchMessage(query, candidate); ok {
				matches = append(matches, match)
			}
		}
	})
	return matches, err
}

func claudeSubagentID(path string) string {
	if !strings.Contains(filepath.ToSlash(path), "/subagents/") {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), "agent-")
}

func searchCodex(path string, query Query) ([]Match, error) {
	var responseMatches, eventMatches []Match
	responseRoles := map[string]bool{}
	state := transcriptMessage{Provider: core.Codex}
	err := scanJSONL(path, func(line []byte) {
		var row struct {
			Type      string          `json:"type"`
			Timestamp time.Time       `json:"timestamp"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &row) != nil {
			return
		}
		switch row.Type {
		case "session_meta":
			var meta struct {
				ID             string          `json:"id"`
				SessionID      string          `json:"session_id"`
				ParentThreadID string          `json:"parent_thread_id"`
				CWD            string          `json:"cwd"`
				Source         json.RawMessage `json:"source"`
			}
			_ = json.Unmarshal(row.Payload, &meta)
			var source struct {
				Subagent *struct {
					ThreadSpawn struct {
						ParentThreadID string `json:"parent_thread_id"`
					} `json:"thread_spawn"`
				} `json:"subagent"`
			}
			_ = json.Unmarshal(meta.Source, &source)
			parentID := meta.ParentThreadID
			if parentID == "" && source.Subagent != nil {
				parentID = source.Subagent.ThreadSpawn.ParentThreadID
			}
			state.SessionID, state.Project = meta.ID, meta.CWD
			if meta.SessionID != "" {
				state.SessionID = meta.SessionID
			}
			if parentID != "" {
				state.SubagentID = meta.ID
				state.SessionID = parentID
			}
		case "turn_context":
			var turn struct {
				CWD string `json:"cwd"`
			}
			_ = json.Unmarshal(row.Payload, &turn)
			if turn.CWD != "" {
				state.Project = turn.CWD
			}
		case "response_item":
			var item struct {
				Type    string          `json:"type"`
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(row.Payload, &item) != nil || item.Type != "message" {
				return
			}
			role := normalizedRole(item.Role)
			if role == "" {
				return
			}
			for _, text := range textContent(item.Content) {
				if codexInjectedContext(text) {
					continue
				}
				// Current Codex rollouts use response_item as the canonical
				// conversation record. Remember that this role is present even
				// when the active query does not match, so a duplicate legacy
				// event_msg cannot leak into the result.
				responseRoles[role] = true
				candidate := state
				candidate.Timestamp, candidate.Role, candidate.Text = row.Timestamp, role, text
				if match, ok := matchMessage(query, candidate); ok {
					responseMatches = append(responseMatches, match)
				}
			}
		case "event_msg":
			var event struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			if json.Unmarshal(row.Payload, &event) != nil {
				return
			}
			role := ""
			switch event.Type {
			case "user_message":
				role = "user"
			case "agent_message":
				role = "assistant"
			}
			if role != "" && event.Message != "" {
				candidate := state
				candidate.Timestamp, candidate.Role, candidate.Text = row.Timestamp, role, event.Message
				if match, ok := matchMessage(query, candidate); ok {
					eventMatches = append(eventMatches, match)
				}
			}
		}
	})
	if err != nil {
		return nil, err
	}
	// Older rollouts emitted event_msg records, while transitional rollouts
	// emitted both shapes for the same message a few milliseconds apart. Prefer
	// response_item independently per role and retain event_msg as a fallback.
	matches := responseMatches
	for _, match := range eventMatches {
		if !responseRoles[match.Role] {
			matches = append(matches, match)
		}
	}
	return matches, nil
}

// Codex records its generated environment envelope as a user response_item.
// It is runtime context, not text authored by the user, so it must not appear
// in transcript search, conversation detail, or prompt exports.
func codexInjectedContext(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<environment_context>")
}

func searchGemini(path string, query Query) ([]Match, error) {
	// #nosec G304 -- path comes from provider discovery under the configured store.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var session struct {
		SessionID string `json:"sessionId"`
		Messages  []struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Timestamp time.Time       `json:"timestamp"`
			Content   json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err = json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	project := ""
	// #nosec G304 -- this marker is adjacent to a discovered provider file.
	if root, readErr := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(path)), ".project_root")); readErr == nil {
		project = strings.TrimSpace(string(root))
	}
	var matches []Match
	for _, message := range session.Messages {
		role := normalizedRole(message.Role)
		if role == "" {
			role = normalizedRole(message.Type)
		}
		if role == "" {
			continue
		}
		for _, text := range textContent(message.Content) {
			candidate := transcriptMessage{Provider: core.Gemini, SessionID: session.SessionID, Project: project, Timestamp: message.Timestamp, Role: role, Text: text}
			if match, ok := matchMessage(query, candidate); ok {
				matches = append(matches, match)
			}
		}
	}
	return matches, nil
}

func searchPi(path string, query Query) ([]Match, error) {
	var matches []Match
	state := transcriptMessage{Provider: core.Pi}
	err := scanJSONL(path, func(line []byte) {
		var row struct {
			Type      string    `json:"type"`
			ID        string    `json:"id"`
			Timestamp time.Time `json:"timestamp"`
			CWD       string    `json:"cwd"`
			Message   struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &row) != nil {
			return
		}
		if row.Type == "session" {
			state.SessionID, state.Project = row.ID, row.CWD
			return
		}
		if row.Type != "message" {
			return
		}
		role := normalizedRole(row.Message.Role)
		if role == "" {
			return
		}
		for _, text := range textContent(row.Message.Content) {
			candidate := state
			candidate.Timestamp, candidate.Role, candidate.Text = row.Timestamp, role, text
			if match, ok := matchMessage(query, candidate); ok {
				matches = append(matches, match)
			}
		}
	})
	return matches, err
}

func searchOpenCode(path string, query Query) ([]Match, error) {
	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", databaseURL.String()+"?mode=ro&_pragma=busy_timeout%3D1000")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var hasParts int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='part'`).Scan(&hasParts); err != nil {
		return nil, err
	}
	if hasParts == 0 {
		return nil, nil
	}
	rows, err := db.Query(`SELECT s.id,s.directory,COALESCE(s.parent_id,''),m.time_created,m.data,p.data
		FROM part p JOIN message m ON m.id=p.message_id JOIN session s ON s.id=m.session_id
		ORDER BY m.time_created,p.id`)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode text parts: %w", err)
	}
	defer rows.Close()
	var matches []Match
	for rows.Next() {
		var sessionID, project, parentID, messageData, partData string
		var created int64
		if err = rows.Scan(&sessionID, &project, &parentID, &created, &messageData, &partData); err != nil {
			return nil, err
		}
		var message struct {
			Role string `json:"role"`
		}
		var part struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			Synthetic bool   `json:"synthetic"`
		}
		if json.Unmarshal([]byte(messageData), &message) != nil || json.Unmarshal([]byte(partData), &part) != nil || part.Type != "text" || part.Synthetic {
			continue
		}
		role := normalizedRole(message.Role)
		if role == "" {
			continue
		}
		candidate := transcriptMessage{Provider: core.OpenCode, SessionID: sessionID, Project: project, Timestamp: time.UnixMilli(created), Role: role, Text: part.Text}
		if parentID != "" {
			candidate.SessionID, candidate.SubagentID = parentID, sessionID
		}
		if match, ok := matchMessage(query, candidate); ok {
			matches = append(matches, match)
		}
	}
	return matches, rows.Err()
}

func normalizedRole(role string) string {
	switch strings.ToLower(role) {
	case "user", "human":
		return "user"
	case "assistant", "agent", "model", "gemini":
		return "assistant"
	default:
		return ""
	}
}

// textContent accepts the string and text-block shapes used by the supported
// providers. It intentionally does not recursively walk arbitrary JSON, which
// would turn tool arguments and results into searchable transcript text.
func textContent(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		if strings.TrimSpace(plain) != "" {
			return []string{plain}
		}
		return nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var out []string
	for _, block := range blocks {
		if (block.Type == "text" || block.Type == "input_text" || block.Type == "output_text" || block.Type == "") && strings.TrimSpace(block.Text) != "" {
			out = append(out, block.Text)
		}
	}
	return out
}

func scanJSONL(path string, visit func([]byte)) error {
	// #nosec G304 -- path comes from provider discovery under a configured store.
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		line, readErr := reader.ReadBytes('\n')
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) > 0 && json.Valid(line) {
			visit(line)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func snippet(text, query string, caseSensitive bool) string {
	text = strings.Join(strings.FieldsFunc(text, unicode.IsSpace), " ")
	haystack, needle := []rune(text), []rune(query)
	if !caseSensitive {
		haystack, needle = []rune(strings.ToLower(text)), []rune(strings.ToLower(query))
	}
	position := runeIndex(haystack, needle)
	if position < 0 {
		position = 0
	}
	original := []rune(text)
	start := max(0, position-60)
	end := min(len(original), position+len(needle)+140)
	result := string(original[start:end])
	if start > 0 {
		result = "…" + result
	}
	if end < len(original) {
		result += "…"
	}
	return result
}

func runeIndex(haystack, needle []rune) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return i
		}
	}
	return -1
}
