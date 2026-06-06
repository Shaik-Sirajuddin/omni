package database

import (
	"database/sql"
	"embed"
	"io/fs"
	"sort"
	"sync"

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

// GetDB returns the singleton SQLite database connection, initializing it on first call.
// The database file is stored at an XDG-compliant data path.
func GetDB() (*sql.DB, error) {
	once.Do(func() {
		path, err := xdg.DataFile("memory/omniagent.db")
		if err != nil {
			dbErr = err
			return
		}
		// Open with a busy timeout and WAL so concurrent writers (e.g. several
		// `omni agent init` / `team init` processes hitting the same DB file at
		// once) wait for the lock instead of failing immediately with
		// SQLITE_BUSY. WAL lets readers proceed while a writer holds the lock.
		conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
		if err != nil {
			dbErr = err
			return
		}
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

func GetReadOnlyDB() (*sql.DB, error) {
	path, err := xdg.DataFile("memory/omniagent.db")
	if err != nil {
		return nil, err
	}

	conn, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}

	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, err
	}

	return conn, nil
}
// ApplySchema runs all embedded schema files against conn in sorted order.
func ApplySchema(conn *sql.DB) error { return applySchema(conn) }

func applySchema(conn *sql.DB) error {
	entries, err := fs.ReadDir(schemaFS, "schema")
	if err != nil {
		return err
	}
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
		if _, err = conn.Exec(string(data)); err != nil {
			return err
		}
	}
	return nil
}
