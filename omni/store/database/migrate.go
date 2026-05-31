package database

import (
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

func runMigrations(db *sql.DB) error {
	if err := ensureMigrationTable(db); err != nil {
		return err
	}

	files, err := fs.ReadDir(schemaFS, "schema/migrations")
	if err != nil {
		return nil
	}

	var indexes []int
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := strings.TrimSuffix(f.Name(), ".sql")
		n, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		indexes = append(indexes, n)
	}
	sort.Ints(indexes)

	if len(indexes) == 0 {
		return nil
	}

	initialized, err := isInitialized(db)
	if err != nil {
		return err
	}

	// Fresh install: base schema already has the final shape — stamp to max and skip.
	if !initialized {
		return stampAndInit(db, indexes[len(indexes)-1])
	}

	// Existing install: run each migration file beyond the current pointer.
	pointer, err := getPointer(db)
	if err != nil {
		return err
	}

	for _, n := range indexes {
		if n <= pointer {
			continue
		}
		path := fmt.Sprintf("schema/migrations/%d.sql", n)
		data, err := schemaFS.ReadFile(path)
		if err != nil {
			return err
		}
		for _, stmt := range strings.Split(string(data), ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := db.Exec(stmt); err != nil {
				if isDuplicateColumnError(err) {
					continue
				}
				return fmt.Errorf("migration %d: %w", n, err)
			}
		}
		if err := setPointer(db, n); err != nil {
			return err
		}
	}
	return nil
}

func isDuplicateColumnError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

func ensureMigrationTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration (
		pointer_value  INTEGER NOT NULL DEFAULT 0,
		is_initialized INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO schema_migration (pointer_value, is_initialized)
		SELECT 0, 0 WHERE NOT EXISTS (SELECT 1 FROM schema_migration)`)
	return err
}

func isInitialized(db *sql.DB) (bool, error) {
	var v int
	err := db.QueryRow(`SELECT is_initialized FROM schema_migration`).Scan(&v)
	return v == 1, err
}

func getPointer(db *sql.DB) (int, error) {
	var v int
	err := db.QueryRow(`SELECT pointer_value FROM schema_migration`).Scan(&v)
	return v, err
}

func setPointer(db *sql.DB, n int) error {
	_, err := db.Exec(`UPDATE schema_migration SET pointer_value = ?`, n)
	return err
}

func stampAndInit(db *sql.DB, max int) error {
	_, err := db.Exec(`UPDATE schema_migration SET pointer_value = ?, is_initialized = 1`, max)
	return err
}
