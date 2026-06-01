package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	provider "github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	"github.com/Shaik-Sirajuddin/omni/store/database"
	"github.com/adrg/xdg"
	"gopkg.in/yaml.v3"
)

//go:embed database/*.sql
var schemaFS embed.FS

type sqlSandboxStore struct {
	db        *sql.DB
	info      Info
	configDir string
}

var (
	storeMu       sync.Mutex
	sandboxStores = map[string]*sqlSandboxStore{}
)

func GetSandboxStore(application string) (SandboxStore, error) {
	if application == "" {
		return nil, fmt.Errorf("sandbox: application is required")
	}

	storeMu.Lock()
	defer storeMu.Unlock()

	if existing, ok := sandboxStores[application]; ok {
		return existing, nil
	}

	db, err := database.GetDB()
	if err != nil {
		return nil, fmt.Errorf("sandbox: init db: %w", err)
	}
	if err := applySchema(db); err != nil {
		return nil, fmt.Errorf("sandbox: migrate sandboxes table: %w", err)
	}

	configDir, err := xdg.DataFile(filepath.Join("memory", "sandboxes", application, ".keep"))
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve config dir: %w", err)
	}
	configDir = filepath.Dir(configDir)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox: create config dir: %w", err)
	}

	store := &sqlSandboxStore{
		db:        db,
		info:      Info{Application: application},
		configDir: configDir,
	}
	sandboxStores[application] = store
	return store, nil
}

func (s *sqlSandboxStore) Info() Info { return s.info }

func (s *sqlSandboxStore) Create(sb *Sandbox) error {
	return s.upsert(sb, false)
}

func (s *sqlSandboxStore) Update(sb *Sandbox) error {
	return s.upsert(sb, true)
}

func (s *sqlSandboxStore) Get(params *GetSandboxParams) (*Sandbox, error) {
	query := `SELECT id, application, pid, active, created_at, config_path FROM sandboxes WHERE application = ?`
	args := []any{s.info.Application}
	if params != nil {
		if params.PID != nil {
			query += ` AND pid = ?`
			args = append(args, *params.PID)
		}
		if params.Name != nil {
			query += ` AND id = ?`
			args = append(args, *params.Name)
		}
		if params.Active {
			query += ` AND active = 1`
		}
	}
	query += ` ORDER BY created_at DESC LIMIT 1`

	row := s.db.QueryRow(query, args...)
	sb, err := s.scanSandbox(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, provider.NoProcessFound
		}
		return nil, err
	}
	return sb, nil
}

func (s *sqlSandboxStore) List() ([]*Sandbox, error) {
	rows, err := s.db.Query(
		`SELECT id, application, pid, active, created_at, config_path
		   FROM sandboxes
		  WHERE application = ?
		  ORDER BY created_at DESC`,
		s.info.Application,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Sandbox
	for rows.Next() {
		sb, err := s.scanSandbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sb)
	}
	return out, rows.Err()
}

func applySchema(db *sql.DB) error {
	entries, err := fs.ReadDir(schemaFS, "database")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := schemaFS.ReadFile("database/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err := db.Exec(string(data)); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqlSandboxStore) upsert(sb *Sandbox, update bool) error {
	if sb == nil || sb.Data == nil || sb.State == nil {
		return fmt.Errorf("sandbox: sandbox metadata is required")
	}
	configPath, err := s.writeConfig(sb.Data.ID, sb.Config)
	if err != nil {
		return err
	}

	query := `INSERT INTO sandboxes (id, application, pid, active, created_at, config_path)
	          VALUES (?, ?, ?, ?, ?, ?)
	          ON CONFLICT(id) DO UPDATE SET
	            application = excluded.application,
	            pid = excluded.pid,
	            active = excluded.active,
	            created_at = excluded.created_at,
	            config_path = excluded.config_path`
	if !update {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM sandboxes WHERE id = ?`, sb.Data.ID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("sandbox: %s already exists", sb.Data.ID)
		}
	}
	_, err = s.db.Exec(query, sb.Data.ID, s.info.Application, sb.State.PID, boolToInt(sb.State.Active), sb.Data.CreatedAt, configPath)
	return err
}

func (s *sqlSandboxStore) writeConfig(id string, cfg *provider.Config) (string, error) {
	if id == "" {
		return "", fmt.Errorf("sandbox: sandbox id is required")
	}
	path := filepath.Join(s.configDir, id+".yaml")
	payload, err := yaml.Marshal(provider.CloneConfig(cfg))
	if err != nil {
		return "", fmt.Errorf("sandbox: marshal config yaml: %w", err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return "", fmt.Errorf("sandbox: write config yaml: %w", err)
	}
	return path, nil
}

func (s *sqlSandboxStore) scanSandbox(scanner interface{ Scan(dest ...any) error }) (*Sandbox, error) {
	var (
		id, application, pid, createdAt, configPath string
		active                                      int
	)
	if err := scanner.Scan(&id, &application, &pid, &active, &createdAt, &configPath); err != nil {
		return nil, err
	}
	cfg, err := s.readConfig(configPath)
	if err != nil {
		return nil, err
	}
	return &provider.Sandbox{
		Config: cfg,
		State: &provider.State{
			PID:    pid,
			Active: active == 1,
		},
		Data: &provider.Data{
			ID:          id,
			Application: application,
			CreatedAt:   createdAt,
		},
	}, nil
}

func (s *sqlSandboxStore) readConfig(path string) (*provider.Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sandbox: read config yaml: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var cfg provider.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("sandbox: parse config yaml: %w", err)
	}
	return &cfg, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
