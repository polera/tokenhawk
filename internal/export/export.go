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

type SpendView struct {
	WindowSpec           string        `json:"window_spec"`
	WindowLabel          string        `json:"window_label"`
	Since                *time.Time    `json:"since,omitempty"`
	Until                time.Time     `json:"until"`
	Provider             core.Provider `json:"provider,omitempty"`
	Search               string        `json:"search,omitempty"`
	Attribution          string        `json:"attribution"`
	TimeseriesResolution string        `json:"timeseries_resolution"`
}

type SpendUsage struct {
	Input           int64 `json:"input_tokens"`
	CachedInput     int64 `json:"cached_input_tokens"`
	CacheCreation   int64 `json:"cache_creation_tokens"`
	CacheCreation1h int64 `json:"cache_creation_1h_tokens"`
	Output          int64 `json:"output_tokens"`
	Reasoning       int64 `json:"reasoning_tokens"`
	Tool            int64 `json:"tool_tokens"`
	Total           int64 `json:"total_tokens"`
}

type SpendCost struct {
	ReportedUSD      float64 `json:"reported_usd"`
	APIRateUSD       float64 `json:"api_rate_usd"`
	TotalUSD         float64 `json:"total_usd"`
	HasUnpricedUsage bool    `json:"has_unpriced_usage"`
}

type SpendAggregate struct {
	Name     string     `json:"name,omitempty"`
	Sessions int        `json:"sessions"`
	Usage    SpendUsage `json:"usage"`
	Cost     SpendCost  `json:"cost"`
}

type SpendPoint struct {
	PeriodStart time.Time  `json:"period_start"`
	PeriodEnd   time.Time  `json:"period_end"`
	Sessions    int        `json:"sessions"`
	Usage       SpendUsage `json:"usage"`
	Cost        SpendCost  `json:"cost"`
}

type SpendReport struct {
	View       SpendView        `json:"view"`
	Totals     SpendAggregate   `json:"totals"`
	Timeseries []SpendPoint     `json:"timeseries"`
	Providers  []SpendAggregate `json:"by_provider"`
	Models     []SpendAggregate `json:"by_model"`
	Days       []SpendAggregate `json:"by_day"`
}

type SpendDocument struct {
	Version    string    `json:"version"`
	Kind       string    `json:"kind"`
	ExportedAt time.Time `json:"exported_at"`
	CostBasis  string    `json:"cost_basis"`
	SpendReport
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

func WriteSpend(path, format string, report SpendReport) error {
	return writeAtomic(path, func(w io.Writer) error {
		switch format {
		case "json":
			return writeSpendJSON(w, report)
		case "csv":
			return writeSpendCSV(w, report)
		default:
			return fmt.Errorf("unsupported export format %q", format)
		}
	})
}

func write(path, format string, sessions []core.Session, conversation []Message, detail bool) error {
	return writeAtomic(path, func(w io.Writer) (err error) {
		switch format {
		case "json":
			if detail {
				return writeDetailJSON(w, sessions, conversation)
			}
			return writeJSON(w, sessions)
		case "csv":
			if detail {
				return writeDetailCSV(w, sessions, conversation)
			}
			return writeCSV(w, sessions)
		default:
			return fmt.Errorf("unsupported export format %q", format)
		}
	})
}

func writeAtomic(path string, render func(io.Writer) error) error {
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
	err = render(tmp)
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

func writeSpendJSON(w io.Writer, report SpendReport) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(SpendDocument{
		Version: "1", Kind: "spend", ExportedAt: time.Now().UTC(),
		CostBasis:   "provider-reported organization billing where available; otherwise public API list-rate USD",
		SpendReport: report,
	})
}

var spendCSVHeader = []string{
	"row_type", "name", "period_start", "period_end", "sessions",
	"input_tokens", "cached_input_tokens", "cache_creation_tokens", "cache_creation_1h_tokens",
	"output_tokens", "reasoning_tokens", "tool_tokens", "total_tokens",
	"reported_cost_usd", "api_rate_cost_usd", "total_cost_usd", "has_unpriced_usage",
	"window_spec", "window_label", "window_since", "window_until", "provider_filter", "search_filter",
	"attribution", "timeseries_resolution",
}

func writeSpendCSV(w io.Writer, report SpendReport) error {
	c := csv.NewWriter(w)
	if err := c.Write(spendCSVHeader); err != nil {
		return err
	}
	if err := c.Write(spendCSVRow("total", report.Totals.Name, time.Time{}, time.Time{}, report.Totals, report.View)); err != nil {
		return err
	}
	for _, point := range report.Timeseries {
		aggregate := SpendAggregate{Sessions: point.Sessions, Usage: point.Usage, Cost: point.Cost}
		if err := c.Write(spendCSVRow("timeseries", "", point.PeriodStart, point.PeriodEnd, aggregate, report.View)); err != nil {
			return err
		}
	}
	for _, section := range []struct {
		rowType string
		rows    []SpendAggregate
	}{{"provider", report.Providers}, {"model", report.Models}, {"day", report.Days}} {
		for _, aggregate := range section.rows {
			if err := c.Write(spendCSVRow(section.rowType, aggregate.Name, time.Time{}, time.Time{}, aggregate, report.View)); err != nil {
				return err
			}
		}
	}
	c.Flush()
	return c.Error()
}

func spendCSVRow(rowType, name string, start, end time.Time, aggregate SpendAggregate, view SpendView) []string {
	date := func(value time.Time) string {
		if value.IsZero() {
			return ""
		}
		return value.UTC().Format(time.RFC3339Nano)
	}
	u, cost := aggregate.Usage, aggregate.Cost
	return []string{
		rowType, name, date(start), date(end), strconv.Itoa(aggregate.Sessions),
		i(u.Input), i(u.CachedInput), i(u.CacheCreation), i(u.CacheCreation1h), i(u.Output), i(u.Reasoning), i(u.Tool), i(u.Total),
		strconv.FormatFloat(cost.ReportedUSD, 'f', 9, 64), strconv.FormatFloat(cost.APIRateUSD, 'f', 9, 64),
		strconv.FormatFloat(cost.TotalUSD, 'f', 9, 64), strconv.FormatBool(cost.HasUnpricedUsage),
		view.WindowSpec, view.WindowLabel, datePointer(view.Since), date(view.Until), string(view.Provider), view.Search,
		view.Attribution, view.TimeseriesResolution,
	}
}

func datePointer(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
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
