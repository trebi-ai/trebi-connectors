package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCreatesExpectedSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "whatsapp-cli.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	cols, err := tableColumns(db.sql, "messages")
	if err != nil {
		t.Fatalf("tableColumns: %v", err)
	}

	for _, want := range []string{
		"chat_name",
		"sender_name",
		"display_text",
		"local_path",
		"downloaded_at",
	} {
		if !cols[want] {
			t.Fatalf("expected messages column %q to exist", want)
		}
	}
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[strings.ToLower(name)] = true
	}
	return cols, rows.Err()
}

func TestRelativeMediaPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "whatsapp-cli.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(dir, "media", "c", "m", "a.jpg")
	if _, err := db.sql.Exec(`INSERT INTO chats(jid, kind) VALUES('c', 'dm')`); err != nil {
		t.Fatal(err)
	}
	for id, p := range map[string]string{"in": inside, "out": "/elsewhere/b.jpg"} {
		if _, err := db.sql.Exec(`INSERT INTO messages(chat_jid, msg_id, ts, from_me, local_path) VALUES('c', ?, 1, 0, ?)`, id, p); err != nil {
			t.Fatal(err)
		}
	}
	// Make the store look like the previous version.
	if _, err := db.sql.Exec(`DELETE FROM schema_migrations WHERE version = 4; PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if db, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for id, want := range map[string]string{"in": filepath.Join("media", "c", "m", "a.jpg"), "out": "/elsewhere/b.jpg"} {
		info, err := db.GetMediaDownloadInfo("c", id)
		if err != nil || info.LocalPath != want {
			t.Fatalf("%s: %q %v, want %q", id, info.LocalPath, err, want)
		}
	}
	var v int
	if err := db.sql.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("user_version %d %v", v, err)
	}
}

func TestNewerSchemaFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "whatsapp-cli.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES(99, 'future', 0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = Open(path)
	var nerr *NewerSchemaError
	if !errors.As(err, &nerr) || nerr.Found != 99 {
		t.Fatalf("Open: %v", err)
	}
}

func TestClearJIDChatNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "whatsapp-cli.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	group := "120363000000000001@g.us"
	if err := db.UpsertChat(group, "group", group, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertChat("1@s.whatsapp.net", "dm", "Ana", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertGroup(group, "Family", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	// Make the store look like the previous version.
	if _, err := db.sql.Exec(`DELETE FROM schema_migrations WHERE version = 5; PRAGMA user_version = 4`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if db, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err := db.sql.QueryRow(`SELECT name FROM chats WHERE jid = ?`, group).Scan(&raw); err != nil || raw != "" {
		t.Fatalf("raw name %q %v", raw, err)
	}
	if c, err := db.GetChat(group); err != nil || c.Name != "Family" {
		t.Fatalf("group: %+v %v", c, err)
	}
	if c, err := db.GetChat("1@s.whatsapp.net"); err != nil || c.Name != "Ana" {
		t.Fatalf("dm: %+v %v", c, err)
	}
}
