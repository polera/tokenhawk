package opencodestore

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenReadOnlyAndDetectCurrentSchema(t *testing.T) {
	// URI metacharacters must remain part of the filename, not query options.
	path := filepath.Join(t.TempDir(), "opencode #1?.db")
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	writer, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`CREATE TABLE session(id TEXT); CREATE TABLE session_v2(id TEXT);`); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	schema, err := Detect(tx)
	if err != nil {
		t.Fatal(err)
	}
	if !schema.V2 || schema.Session != "session_v2" || schema.Message != "session_message" {
		t.Fatalf("legacy tables shadowed current schema: %#v", schema)
	}
	if _, err := tx.Exec(`INSERT INTO session_v2 VALUES('must-not-write')`); err == nil {
		t.Fatal("source database was writable")
	}
}

func TestOpenDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err == nil {
		t.Fatal("missing source opened successfully")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source was created: %v", err)
	}
}
