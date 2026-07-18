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

func Write(path, format string, sessions []core.Session) error {
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
		err = writeJSON(tmp, sessions)
	case "csv":
		err = writeCSV(tmp, sessions)
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
	return e.Encode(Document{Version: "1", ExportedAt: time.Now().UTC(), CostBasis: "estimated API-equivalent USD", Sessions: s})
}
func writeCSV(w io.Writer, s []core.Session) error {
	c := csv.NewWriter(w)
	defer c.Flush()
	header := []string{"provider", "session_id", "project", "started_at", "updated_at", "active", "row_type", "subagent_id", "subagent_name", "agent_path", "agent_status", "agent_running", "running_subagents", "total_subagents", "model", "input_tokens", "cached_input_tokens", "cache_creation_tokens", "output_tokens", "reasoning_tokens", "tool_tokens", "total_tokens", "estimated_cost_usd", "pricing_status", "source_health"}
	if err := c.Write(header); err != nil {
		return err
	}
	for _, x := range s {
		for _, u := range x.Usage {
			row := usageRow(x, nil, u)
			if err := c.Write(row); err != nil {
				return err
			}
		}
		for ai := range x.Subagents {
			a := &x.Subagents[ai]
			for _, u := range a.Usage {
				if err := c.Write(usageRow(x, a, u)); err != nil {
					return err
				}
			}
		}
	}
	return c.Error()
}

func usageRow(x core.Session, a *core.Subagent, u core.Usage) []string {
	started, updated, active, rowType, subagentID, name, agentPath, status, running, health := x.StartedAt, x.UpdatedAt, x.Active, "session", "", "", "", "", "", x.SourceHealth
	if a != nil {
		started, updated, active, rowType = a.StartedAt, a.UpdatedAt, a.Running, "subagent"
		subagentID, name, agentPath, status, running, health = a.ID, a.Name, a.AgentPath, a.Status, strconv.FormatBool(a.Running), a.SourceHealth
	}
	return []string{string(x.Provider), x.ID, x.Project, started.UTC().Format(time.RFC3339Nano), updated.UTC().Format(time.RFC3339Nano), strconv.FormatBool(active), rowType, subagentID, name, agentPath, status, running, strconv.Itoa(x.RunningSubagents()), strconv.Itoa(len(x.Subagents)), u.Model, i(u.Input), i(u.CachedInput), i(u.CacheCreation), i(u.Output), i(u.Reasoning), i(u.Tool), i(u.Total), strconv.FormatFloat(u.CostUSD, 'f', 6, 64), u.PricingStatus, health}
}
func i(v int64) string { return strconv.FormatInt(v, 10) }
