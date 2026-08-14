package sessionsearch

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/polera/tokenhawk/internal/core"
)

// Antigravity CLI conversations are SQLite databases whose steps carry
// protobuf payloads. Only the documented text locations below are decoded;
// everything else — including reasoning summaries (field 20.3), tool
// invocations (field 20.7), and account or quota data — stays opaque, in
// line with the privacy model used for every other provider.
//
//	field 1       step type
//	field 5.1.1   step start time, UTC seconds
//	field 19.2    user-authored text (user input steps)
//	field 20.1    assistant prose text (model response steps)
const (
	agyStepUserInput = 14
	agyStepResponse  = 15
)

func searchAgy(path string, query Query) ([]Match, error) {
	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", databaseURL.String()+"?mode=ro&_pragma=busy_timeout%3D1000")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var hasSteps int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='steps'`).Scan(&hasSteps); err != nil {
		return nil, err
	}
	if hasSteps == 0 {
		return nil, nil
	}
	sessionID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	project := agyProject(db)
	rows, err := db.Query(`SELECT step_type, step_payload FROM steps WHERE step_type IN (?,?) AND step_payload IS NOT NULL ORDER BY idx`, agyStepUserInput, agyStepResponse)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var matches []Match
	for rows.Next() {
		var stepType int
		var payload []byte
		if err = rows.Scan(&stepType, &payload); err != nil {
			return nil, err
		}
		role, text := "", ""
		switch stepType {
		case agyStepUserInput:
			role = "user"
			text, _ = protoString(payload, 19, 2)
		case agyStepResponse:
			// Response steps that propose tool calls carry no prose text and
			// are skipped below.
			role = "assistant"
			text, _ = protoString(payload, 20, 1)
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		var when time.Time
		if seconds, ok := protoVarint(payload, 5, 1, 1); ok && seconds > 0 {
			when = time.Unix(int64(seconds), 0).UTC() // #nosec G115 -- epoch seconds fit in int64
		}
		candidate := transcriptMessage{Provider: core.Agy, SessionID: sessionID, Project: project, Timestamp: when, Role: role, Text: text}
		if match, ok := matchMessage(query, candidate); ok {
			matches = append(matches, match)
		}
	}
	return matches, rows.Err()
}

// agyProject reads the conversation's workspace URI from the trajectory
// metadata blob (field 7, falling back to field 1.1).
func agyProject(db *sql.DB) string {
	var hasBlob int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='trajectory_metadata_blob'`).Scan(&hasBlob); err != nil || hasBlob == 0 {
		return ""
	}
	var blob []byte
	if err := db.QueryRow(`SELECT data FROM trajectory_metadata_blob LIMIT 1`).Scan(&blob); err != nil {
		return ""
	}
	uri, ok := protoString(blob, 7)
	if !ok || uri == "" {
		uri, _ = protoString(blob, 1, 1)
	}
	return strings.TrimPrefix(uri, "file://")
}
