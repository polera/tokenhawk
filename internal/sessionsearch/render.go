package sessionsearch

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func Write(w io.Writer, format string, report Report) error {
	switch format {
	case "plain":
		return writePlain(w, report)
	case "json":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	default:
		return fmt.Errorf("unsupported search format %q (expected plain or json)", format)
	}
}

func writePlain(w io.Writer, report Report) error {
	if len(report.Matches) == 0 {
		_, err := fmt.Fprintln(w, "no matches")
		return err
	}
	for _, match := range report.Matches {
		when := "unknown time"
		if !match.Timestamp.IsZero() {
			when = match.Timestamp.Local().Format("2006-01-02 15:04")
		}
		session := match.SessionID
		if match.SubagentID != "" {
			session += "/" + match.SubagentID
		}
		header := fmt.Sprintf("%s  %-8s  %-9s  %s", when, match.Provider, match.Role, session)
		if match.Project != "" {
			header += "  " + match.Project
		}
		if _, err := fmt.Fprintf(w, "%s\n  %s\n", header, strings.TrimSpace(match.Snippet)); err != nil {
			return err
		}
	}
	return nil
}
