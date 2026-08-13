package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/polera/tokenhawk/internal/core"
)

type Document struct {
	Version    string         `json:"version"`
	ExportedAt time.Time      `json:"exported_at"`
	CostBasis  string         `json:"cost_basis"`
	Sessions   []core.Session `json:"sessions"`
}

type Message struct {
	Timestamp  time.Time `json:"timestamp,omitempty"`
	SubagentID string    `json:"subagent_id,omitempty"`
	Role       string    `json:"role"`
	Text       string    `json:"text"`
}

type DetailDocument struct {
	Document
	Conversation []Message `json:"conversation,omitempty"`
}

func Write(path, format string, sessions []core.Session) error {
	return write(path, format, sessions, nil, false)
}

// WriteDetail exports one session and, when loaded by the caller, its visible
// conversation history. Bulk exports deliberately continue to omit transcript
// content.
func WriteDetail(path, format string, session core.Session, conversation []Message) error {
	return write(path, format, []core.Session{session}, conversation, true)
}

func write(path, format string, sessions []core.Session, conversation []Message, detail bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tokenhawk-export-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	switch format {
	case "json":
		if detail {
			err = writeDetailJSON(tmp, sessions, conversation)
		} else {
			err = writeJSON(tmp, sessions)
		}
	case "csv":
		if detail {
			err = writeDetailCSV(tmp, sessions, conversation)
		} else {
			err = writeCSV(tmp, sessions)
		}
	default:
		err = fmt.Errorf("unsupported export format %q", format)
	}
	if err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}
func writeJSON(w io.Writer, s []core.Session) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(document(s))
}

func writeDetailJSON(w io.Writer, sessions []core.Session, conversation []Message) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(DetailDocument{Document: document(sessions), Conversation: conversation})
}

func document(sessions []core.Session) Document {
	return Document{Version: "2", ExportedAt: time.Now().UTC(), CostBasis: "public API list-rate USD", Sessions: sessions}
}

var csvHeader = []string{"provider", "session_id", "project", "started_at", "updated_at", "active", "row_type", "subagent_id", "subagent_name", "agent_path", "agent_status", "agent_running", "running_subagents", "total_subagents", "model", "input_tokens", "cached_input_tokens", "cache_creation_tokens", "cache_creation_1h_tokens", "output_tokens", "reasoning_tokens", "tool_tokens", "total_tokens", "api_cost_usd", "pricing_status", "source_health"}

func writeCSV(w io.Writer, s []core.Session) error {
	c := csv.NewWriter(w)
	if err := c.Write(csvHeader); err != nil {
		return err
	}
	if err := writeCSVUsage(c, s, 0); err != nil {
		return err
	}
	c.Flush()
	return c.Error()
}

func writeDetailCSV(w io.Writer, sessions []core.Session, conversation []Message) error {
	c := csv.NewWriter(w)
	header := append(append([]string(nil), csvHeader...), "message_role", "message_timestamp", "message_text")
	if err := c.Write(header); err != nil {
		return err
	}
	if err := writeCSVUsage(c, sessions, 3); err != nil {
		return err
	}
	if len(sessions) > 0 {
		session := sessions[0]
		for _, message := range conversation {
			row := make([]string, len(header))
			row[0], row[1], row[2] = string(session.Provider), session.ID, session.Project
			row[5], row[6], row[25] = strconv.FormatBool(session.Active), "message", session.SourceHealth
			row[26], row[27], row[28] = message.Role, message.Timestamp.UTC().Format(time.RFC3339Nano), message.Text
			row[7] = message.SubagentID
			if err := c.Write(row); err != nil {
				return err
			}
		}
	}
	c.Flush()
	return c.Error()
}

func writeCSVUsage(c *csv.Writer, sessions []core.Session, extraColumns int) error {
	for _, x := range sessions {
		for _, u := range x.Usage {
			row := append(usageRow(x, nil, u), make([]string, extraColumns)...)
			if err := c.Write(row); err != nil {
				return err
			}
		}
		for ai := range x.Subagents {
			a := &x.Subagents[ai]
			for _, u := range a.Usage {
				row := append(usageRow(x, a, u), make([]string, extraColumns)...)
				if err := c.Write(row); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func usageRow(x core.Session, a *core.Subagent, u core.Usage) []string {
	started, updated, active, rowType, subagentID, name, agentPath, status, running, health := x.StartedAt, x.UpdatedAt, x.Active, "session", "", "", "", "", "", x.SourceHealth
	if a != nil {
		started, updated, active, rowType = a.StartedAt, a.UpdatedAt, a.Running, "subagent"
		subagentID, name, agentPath, status, running, health = a.ID, a.Name, a.AgentPath, a.Status, strconv.FormatBool(a.Running), a.SourceHealth
	}
	return []string{string(x.Provider), x.ID, x.Project, started.UTC().Format(time.RFC3339Nano), updated.UTC().Format(time.RFC3339Nano), strconv.FormatBool(active), rowType, subagentID, name, agentPath, status, running, strconv.Itoa(x.RunningSubagents()), strconv.Itoa(len(x.Subagents)), u.Model, i(u.Input), i(u.CachedInput), i(u.CacheCreation), i(u.CacheCreation1h), i(u.Output), i(u.Reasoning), i(u.Tool), i(u.Total), strconv.FormatFloat(u.CostUSD, 'f', 6, 64), u.PricingStatus, health}
}
func i(v int64) string { return strconv.FormatInt(v, 10) }
