package statusline

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/polera/tokenhawk/internal/core"
)

const (
	highInputAlarmTokens = 100_000
	minimumCacheRatio    = 0.80

	blue  = "\x1b[38;2;5;169;199m"
	white = "\x1b[38;2;248;248;242m"
	red   = "\x1b[38;2;255;92;87m"
	bold  = "\x1b[1m"
	reset = "\x1b[0m"
)

// Selector identifies one provider session for a compact status display.
type Selector struct {
	Provider  core.Provider
	SessionID string
	Project   string
	Status    string
}

// ClaudeInput is the subset of Claude Code's status-line JSON that Tokenhawk
// needs. Claude keeps session_id stable for the lifetime of the session.
type ClaudeInput struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
}

// ParseClaude reads the JSON object Claude Code supplies to statusLine commands.
func ParseClaude(r io.Reader) (Selector, error) {
	var input ClaudeInput
	if err := json.NewDecoder(r).Decode(&input); err != nil {
		return Selector{}, fmt.Errorf("read Claude status-line input: %w", err)
	}
	project := input.Workspace.CurrentDir
	if project == "" {
		project = input.CWD
	}
	return Selector{Provider: core.Claude, SessionID: input.SessionID, Project: project}, nil
}

// Select returns one session, never a sum across sessions. Exact session ID is
// preferred; wrappers without an ID use the most recently updated matching
// project session, with active sessions preferred.
func Select(sessions []core.Session, selector Selector) (core.Session, bool) {
	var candidates []core.Session
	for _, session := range sessions {
		if selector.Provider != "" && session.Provider != selector.Provider {
			continue
		}
		if selector.Status == "active" && !session.Active || selector.Status == "inactive" && session.Active {
			continue
		}
		if selector.SessionID != "" && session.ID != selector.SessionID {
			continue
		}
		if selector.Project != "" && !sameProject(session.Project, selector.Project) {
			continue
		}
		candidates = append(candidates, session)
	}
	if len(candidates) == 0 && selector.SessionID != "" && selector.Project != "" {
		// A session ID is globally authoritative within a provider. Some early
		// records do not contain a project, so do not reject an exact ID because
		// its project metadata is missing or stale.
		fallback := selector
		fallback.Project = ""
		return Select(sessions, fallback)
	}
	if len(candidates) == 0 {
		return core.Session{}, false
	}
	best := candidates[0]
	for _, session := range candidates[1:] {
		if session.Active != best.Active {
			if session.Active {
				best = session
			}
			continue
		}
		if session.UpdatedAt.After(best.UpdatedAt) {
			best = session
		}
	}
	return best, true
}

func sameProject(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return filepath.Clean(aa) == filepath.Clean(bb)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// Render formats one session as plain text, ANSI text, tmux format text, or JSON.
func Render(session core.Session, format string) (string, error) {
	if format == "json" {
		return renderJSON(session)
	}
	if format != "plain" && format != "ansi" && format != "tmux" {
		return "", fmt.Errorf("unsupported status format %q (expected plain, ansi, tmux, or json)", format)
	}
	total := session.Totals()
	cacheRatio := cachePercentage(total)
	cost := costText(total)
	agents := fmt.Sprintf("%d/%d agents", session.RunningSubagents(), len(session.Subagents))
	metrics := fmt.Sprintf("%s · in %s · cache %.1f%% · out %s · I:O %s · %s · %s",
		session.Provider, human(total.Input), cacheRatio, human(total.Output), ratioText(total.Input, total.Output), cost, agents)
	alarm := session.Active && cacheAlarm(total)
	switch format {
	case "ansi":
		if alarm {
			return red + bold + "⚠ TOKENHAWK LOW CACHE" + reset + white + "  " + metrics + reset, nil
		}
		return blue + bold + "TOKENHAWK" + reset + white + "  " + metrics + reset, nil
	case "tmux":
		if alarm {
			return "#[fg=#ff5c57,bold]⚠ TOKENHAWK LOW CACHE#[fg=#f8f8f2,nobold]  " + metrics, nil
		}
		return "#[fg=#05A9C7,bold]TOKENHAWK#[fg=#f8f8f2,nobold]  " + metrics, nil
	default:
		if alarm {
			return "⚠ TOKENHAWK LOW CACHE  " + metrics, nil
		}
		return "TOKENHAWK  " + metrics, nil
	}
}

// Waiting renders a non-error placeholder suitable for status commands that
// run before a provider has written its first session record.
func Waiting(selector Selector, format string) (string, error) {
	provider := string(selector.Provider)
	if provider == "" {
		provider = "active"
	}
	message := fmt.Sprintf("TOKENHAWK  waiting for %s session…", provider)
	switch format {
	case "plain":
		return message, nil
	case "ansi":
		return blue + bold + "TOKENHAWK" + reset + white + fmt.Sprintf("  waiting for %s session…", provider) + reset, nil
	case "tmux":
		return fmt.Sprintf("#[fg=#05A9C7,bold]TOKENHAWK#[fg=#888888,nobold]  waiting for %s session…", provider), nil
	case "json":
		data, err := json.Marshal(struct {
			Provider string `json:"provider"`
			Status   string `json:"status"`
		}{Provider: provider, Status: "waiting"})
		return string(data), err
	default:
		return "", fmt.Errorf("unsupported status format %q (expected plain, ansi, tmux, or json)", format)
	}
}

func renderJSON(session core.Session) (string, error) {
	total := session.Totals()
	data, err := json.Marshal(struct {
		Provider         core.Provider `json:"provider"`
		SessionID        string        `json:"session_id"`
		Project          string        `json:"project"`
		Active           bool          `json:"active"`
		Input            int64         `json:"input_tokens"`
		CachedInput      int64         `json:"cached_input_tokens"`
		CacheRatio       float64       `json:"cache_ratio"`
		Output           int64         `json:"output_tokens"`
		InputOutputRatio string        `json:"input_output_ratio"`
		CostUSD          float64       `json:"estimated_cost_usd"`
		PricingStatus    string        `json:"pricing_status"`
		RunningSubagents int           `json:"running_subagents"`
		TotalSubagents   int           `json:"total_subagents"`
		CacheAlarm       bool          `json:"cache_alarm"`
	}{
		Provider: session.Provider, SessionID: session.ID, Project: session.Project, Active: session.Active,
		Input: total.Input, CachedInput: total.CachedInput, CacheRatio: cachePercentage(total) / 100,
		Output: total.Output, InputOutputRatio: ratioText(total.Input, total.Output), CostUSD: total.CostUSD,
		PricingStatus: total.PricingStatus, RunningSubagents: session.RunningSubagents(),
		TotalSubagents: len(session.Subagents), CacheAlarm: session.Active && cacheAlarm(total),
	})
	return string(data), err
}

func cachePercentage(usage core.Usage) float64 {
	if usage.Input == 0 {
		return 0
	}
	return float64(usage.CachedInput) / float64(usage.Input) * 100
}

func cacheAlarm(usage core.Usage) bool {
	return usage.Input >= highInputAlarmTokens && cachePercentage(usage)/100 < minimumCacheRatio
}

func human(value int64) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(value)/1e6)
	case value >= 1_000:
		return fmt.Sprintf("%.1fk", float64(value)/1e3)
	default:
		return fmt.Sprint(value)
	}
}

func ratioText(input, output int64) string {
	switch {
	case input == 0 && output == 0:
		return "-"
	case output == 0:
		return "∞:1"
	case input == 0:
		return "0:1"
	case input >= output:
		return compactDecimal(float64(input)/float64(output)) + ":1"
	default:
		return "1:" + compactDecimal(float64(output)/float64(input))
	}
}

func compactDecimal(value float64) string {
	if value >= 100 {
		return fmt.Sprintf("%.0f", value)
	}
	if value >= 10 {
		return fmt.Sprintf("%.1f", value)
	}
	return fmt.Sprintf("%.2f", value)
}

func costText(usage core.Usage) string {
	switch {
	case usage.PricingStatus == "priced" || usage.PricingStatus == "reported":
		return fmt.Sprintf("$%.4f", usage.CostUSD)
	case usage.CostUSD > 0:
		return fmt.Sprintf("$%.4f+", usage.CostUSD)
	default:
		return "unpriced"
	}
}
