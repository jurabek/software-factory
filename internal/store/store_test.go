package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOpenRejectsLegacyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`create table campaigns (id text primary key)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(path)
	if !errors.Is(err, ErrStateIncompatible) {
		t.Fatalf("err = %v, want state incompatibility", err)
	}
}
