package core

import "time"

type Provider string

const (
	Claude   Provider = "claude"
	Codex    Provider = "codex"
	Gemini   Provider = "gemini"
	Pi       Provider = "pi"
	OpenCode Provider = "opencode"
)

type Usage struct {
	Model         string  `json:"model"`
	Input         int64   `json:"input_tokens"`
	CachedInput   int64   `json:"cached_input_tokens"`
	CacheCreation int64   `json:"cache_creation_tokens"`
	Output        int64   `json:"output_tokens"`
	Reasoning     int64   `json:"reasoning_tokens"`
	Tool          int64   `json:"tool_tokens"`
	Total         int64   `json:"total_tokens"`
	CostUSD       float64 `json:"estimated_cost_usd"`
	PricingStatus string  `json:"pricing_status"`
}

type Session struct {
	Provider     Provider   `json:"provider"`
	ID           string     `json:"session_id"`
	Project      string     `json:"project"`
	StartedAt    time.Time  `json:"started_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Active       bool       `json:"active"`
	SourceHealth string     `json:"source_health"`
	SourcePath   string     `json:"source_path,omitempty"`
	Usage        []Usage    `json:"usage_by_model"`
	Subagents    []Subagent `json:"subagents,omitempty"`
}

type Subagent struct {
	ID           string    `json:"subagent_id"`
	ParentID     string    `json:"parent_session_id"`
	Name         string    `json:"name,omitempty"`
	AgentPath    string    `json:"agent_path,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Status       string    `json:"status"`
	Running      bool      `json:"running"`
	SourceHealth string    `json:"source_health"`
	Usage        []Usage   `json:"usage_by_model"`
}

func (s Session) Totals() Usage {
	all := append([]Usage(nil), s.Usage...)
	for _, a := range s.Subagents {
		all = append(all, a.Usage...)
	}
	return usageTotals(all)
}

func (s Session) DirectTotals() Usage {
	return usageTotals(s.Usage)
}

func (a Subagent) Totals() Usage {
	return usageTotals(a.Usage)
}

func (s Session) RunningSubagents() int {
	n := 0
	for _, a := range s.Subagents {
		if a.Running {
			n++
		}
	}
	return n
}

func usageTotals(usage []Usage) Usage {
	var out Usage
	if len(usage) == 0 {
		out.PricingStatus = "unpriced"
		return out
	}
	allReported := true
	allKnown := true
	for _, u := range usage {
		out.Input += u.Input
		out.CachedInput += u.CachedInput
		out.CacheCreation += u.CacheCreation
		out.Output += u.Output
		out.Reasoning += u.Reasoning
		out.Tool += u.Tool
		out.Total += u.Total
		out.CostUSD += u.CostUSD
		allReported = allReported && u.PricingStatus == "reported"
		allKnown = allKnown && (u.PricingStatus == "priced" || u.PricingStatus == "reported")
	}
	switch {
	case allReported:
		out.PricingStatus = "reported"
	case allKnown:
		out.PricingStatus = "priced"
	default:
		out.PricingStatus = "partially priced"
	}
	return out
}

type Filter struct {
	Provider Provider
	Model    string
	Project  string
	Status   string
	Since    time.Time
	Until    time.Time
}

type SourceState struct {
	Path        string
	Provider    Provider
	SessionID   string
	Size        int64
	ModTimeNS   int64
	Offset      int64
	ParserState string
	Kind        string
	ParentID    string
	Name        string
	AgentPath   string
}

type Parsed struct {
	Session     Session
	Subagent    *Subagent
	Provider    Provider
	SourcePath  string
	Offset      int64
	ParserState string
	Replace     bool
}
