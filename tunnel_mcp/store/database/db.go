package database

import (
	"database/sql"
	"embed"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/adrg/xdg"
	_ "modernc.org/sqlite"
)

//go:embed schema
var schemaFS embed.FS

var (
	once  sync.Once
	db    *sql.DB
	dbErr error
)

// GetDefaultDB returns the singleton using the XDG data path ~/.local/share/memory/mcp.db
func GetDefaultDB() (*sql.DB, error) {
	if path := os.Getenv("AXO_LINK_MCP_DB_PATH"); path != "" {
		return GetDB(path)
	}
	path, err := xdg.DataFile("memory/mcp.db")
	if err != nil {
		return nil, err
	}
	return GetDB(path)
}

// GetDB returns the singleton SQLite message-store connection, applying schema on first call.
func GetDB(path string) (*sql.DB, error) {
	once.Do(func() {
		// _busy_timeout=5000: retry for up to 5 s before returning SQLITE_BUSY.
		// _journal_mode=WAL: allows concurrent readers while a writer holds the lock.
		dsn := path + "?_busy_timeout=5000&_journal_mode=WAL"
		conn, err := sql.Open("sqlite", dsn)
		if err != nil {
			dbErr = err
			return
		}
		// Single writer connection prevents go/database/sql from opening a
		// second connection that would bypass the in-process mutex and contend
		// on the file lock.
		conn.SetMaxOpenConns(1)
		if err := applySchema(conn); err != nil {
			dbErr = err
			conn.Close()
			return
		}
		if err := runMigrations(conn); err != nil {
			dbErr = err
			conn.Close()
			return
		}
		db = conn
	})
	return db, dbErr
}

// ApplySchema runs all embedded schema files against conn in sorted order.
func ApplySchema(conn *sql.DB) error { return applySchema(conn) }

func applySchema(conn *sql.DB) error {
	entries, err := fs.ReadDir(schemaFS, "schema")
	if err != nil {
		return err
	}
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := schemaFS.ReadFile("schema/" + e.Name())
		if err != nil {
			return err
		}
		statements := strings.Split(string(data), ";")
		for _, statement := range statements {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			if _, err = tx.Exec(statement); err != nil {
				if isDuplicateColumnError(err) {
					continue
				}
				return err
			}
		}
	}
	return tx.Commit()
}

// WithTestDB opens an in-memory SQLite database with the schema applied.
// The connection is closed automatically when the test finishes.
func WithTestDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	conn.SetMaxOpenConns(1)
	if err := applySchema(conn); err != nil {
		conn.Close()
		t.Fatalf("apply schema: %v", err)
	}
	if err := runMigrations(conn); err != nil {
		conn.Close()
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}
