// Package opencodestore handles the on-disk layouts of OpenCode's local store.
package opencodestore

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Schema struct {
	Session, Message, Agent string
	V2                      bool
}

func Open(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return sql.Open("sqlite", u.String()+"?mode=ro&_pragma=busy_timeout%3D1000")
}

// Detect prefers the current projections when legacy tables remain after an
// upgrade. All identifiers returned here are fixed literals, not user input.
func Detect(tx *sql.Tx) (Schema, error) {
	var current, legacy int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='session_v2'`).Scan(&current); err != nil {
		return Schema{}, err
	}
	if current > 0 {
		return Schema{Session: "session_v2", Message: "session_message", Agent: "agent", V2: true}, nil
	}
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='session'`).Scan(&legacy); err != nil {
		return Schema{}, err
	}
	if legacy == 0 {
		return Schema{}, fmt.Errorf("unsupported OpenCode database: no session or session_v2 table")
	}
	s := Schema{Session: "session", Message: "message", Agent: "NULL"}
	rows, err := tx.Query(`PRAGMA table_info(session)`)
	if err != nil {
		return Schema{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			return Schema{}, err
		}
		if name == "agent" {
			s.Agent = "agent"
		}
	}
	return s, rows.Err()
}
