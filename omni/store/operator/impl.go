package operator

import (
	"database/sql"
	"fmt"
	"sync"

	"github.com/Shaik-Sirajuddin/memory/pkg/log"
	"github.com/Shaik-Sirajuddin/memory/omniagent"
	"github.com/Shaik-Sirajuddin/memory/operator"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
	"github.com/Shaik-Sirajuddin/memory/store/database"
)

var logger = log.NewLogger("component", "store")


type sqlStore struct {
	db *sql.DB
}

var (
	storeOnce sync.Once
	opStore   *sqlStore
	storeErr  error

	readOnlyStoreOnce sync.Once
	readOnlyOpStore   *sqlStore
	readOnlyStoreErr  error
)

// GetOperatorStore returns the singleton read/write OperatorStore, initializing it on first call.
// It reuses the omniagent DB singleton and allows schema initialization/migrations through database.GetDB().
func GetOperatorStore() (OperatorStore, error) {
	storeOnce.Do(func() {
		logger.Debug("GetOperatorStore: initialising store singleton")

		db, err := database.GetDB()
		if err != nil {
			logger.Error("GetOperatorStore: database init failed", "err", err)
			storeErr = err
			return
		}

		opStore = &sqlStore{db: db}

		logger.Info("GetOperatorStore: store ready")
	})

	return opStore, storeErr
}

// GetReadOnlyOperatorStore returns the singleton read-only OperatorStore, initializing it on first call.
//
// This is intended for viewers, inspectors, dashboards, or CLI commands that only
// need to read existing operator/workspace data.
//
// It opens the omniagent SQLite database with mode=ro, so:
//   - the DB file must already exist
//   - writes will fail
//   - schema migrations are not applied here
//
// Use GetOperatorStore for normal read/write app usage.
func GetReadOnlyOperatorStore() (OperatorStore, error) {
	readOnlyStoreOnce.Do(func() {
		logger.Debug("GetReadOnlyOperatorStore: initialising read-only store singleton")

		db, err := database.GetReadOnlyDB()
		if err != nil {
			logger.Error("GetReadOnlyOperatorStore: read-only database init failed", "err", err)
			readOnlyStoreErr = err
			return
		}

		readOnlyOpStore = &sqlStore{db: db}

		logger.Info("GetReadOnlyOperatorStore: read-only store ready")
	})

	return readOnlyOpStore, readOnlyStoreErr
}

// NewWithDB creates an OperatorStore backed by the provided database connection.
// Intended for use in tests where a pre-seeded DB is preferred over the singleton.
func NewWithDB(db *sql.DB) OperatorStore {
	return &sqlStore{db: db}
}

// --- Workspace CRUD ---

func (s *sqlStore) CreateWorkspace(ws *operator.TeamInfo) error {
	logger.Debug("CreateWorkspace: insert", "workspaceID", ws.ID, "workspaceDir", ws.WorkspaceDir)
	remote := ws.Remote
	if remote == "" {
		remote = "localhost"
	}
	_, err := s.db.Exec(
		`INSERT INTO workspaces (id, name, remote, workspace_dir) VALUES (?, ?, ?, ?)`,
		ws.ID, ws.Name, remote, ws.WorkspaceDir,
	)
	if err != nil {
		logger.Error("CreateWorkspace: insert failed", "workspaceID", ws.ID, "workspaceDir", ws.WorkspaceDir, "err", err)
		return err
	}
	logger.Info("CreateWorkspace: inserted", "workspaceID", ws.ID, "workspaceDir", ws.WorkspaceDir)
	return err
}

func (s *sqlStore) GetWorkspace(id string) (*operator.TeamInfo, error) {
	logger.Debug("GetWorkspace(store): query", "workspaceID", id)
	row := s.db.QueryRow(
		`SELECT w.id, w.name, w.remote, w.workspace_dir,
		        (SELECT COUNT(*) FROM agents a WHERE a.workspace_dir = w.workspace_dir)
		 FROM workspaces w WHERE w.id = ?`, id,
	)
	ws, err := scanWorkspace(row)
	if err != nil {
		logger.Error("GetWorkspace(store): query failed", "workspaceID", id, "err", err)
		return nil, err
	}
	logger.Info("GetWorkspace(store): loaded", "workspaceID", ws.ID, "agentCount", ws.Agents)
	return ws, nil
}

func (s *sqlStore) WorkspaceByDir(dir sandbox.WorkspaceDir) (*operator.TeamInfo, error) {
	logger.Debug("WorkspaceByDir: query", "workspaceDir", dir)
	row := s.db.QueryRow(
		`SELECT w.id, w.name, w.remote, w.workspace_dir,
		        (SELECT COUNT(*) FROM agents a WHERE a.workspace_dir = w.workspace_dir)
		 FROM workspaces w WHERE w.workspace_dir = ?`, string(dir),
	)
	ws, err := scanWorkspace(row)
	if err != nil {
		logger.Debug("WorkspaceByDir: query failed", "workspaceDir", dir, "err", err)
		return nil, err
	}
	logger.Info("WorkspaceByDir: loaded", "workspaceID", ws.ID, "workspaceDir", ws.WorkspaceDir)
	return ws, nil
}

func (s *sqlStore) ListWorkspaces() ([]*operator.TeamInfo, error) {
	logger.Debug("ListWorkspaces(store): query")
	rows, err := s.db.Query(
		`SELECT w.id, w.name, w.remote, w.workspace_dir,
		        (SELECT COUNT(*) FROM agents a WHERE a.workspace_dir = w.workspace_dir)
		 FROM workspaces w`,
	)
	if err != nil {
		logger.Error("ListWorkspaces(store): query failed", "err", err)
		return nil, err
	}
	defer rows.Close()

	var teams []*operator.TeamInfo
	for rows.Next() {
		var t operator.TeamInfo
		if err := rows.Scan(&t.ID, &t.Name, &t.Remote, &t.WorkspaceDir, &t.Agents); err != nil {
			logger.Error("ListWorkspaces(store): scan failed", "err", err)
			return nil, err
		}
		teams = append(teams, &t)
	}
	if err := rows.Err(); err != nil {
		logger.Error("ListWorkspaces(store): rows iteration failed", "err", err)
		return nil, err
	}
	logger.Info("ListWorkspaces(store): completed", "count", len(teams))
	return teams, nil
}

func (s *sqlStore) DeleteWorkspace(id string) error {
	logger.Debug("DeleteWorkspace: delete", "workspaceID", id)
	res, err := s.db.Exec(`DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		logger.Error("DeleteWorkspace: delete failed", "workspaceID", id, "err", err)
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		logger.Error("DeleteWorkspace: rows affected failed", "workspaceID", id, "err", err)
		return err
	}
	if n == 0 {
		logger.Error("DeleteWorkspace: workspace not found", "workspaceID", id)
		return fmt.Errorf("operator: workspace %q not found", id)
	}
	logger.Info("DeleteWorkspace: completed", "workspaceID", id)
	return nil
}

// --- Agent operations (omniagent's agents table) ---

func (s *sqlStore) ListAgentsByDir(dir sandbox.WorkspaceDir) ([]*omniagent.AgentInfo, error) {
	logger.Debug("ListAgentsByDir: query", "workspaceDir", dir)
	rows, err := s.db.Query(
		`SELECT id, name, workspace_dir, memory_dir FROM agents WHERE workspace_dir = ?`,
		string(dir),
	)
	if err != nil {
		logger.Error("ListAgentsByDir: query failed", "workspaceDir", dir, "err", err)
		return nil, err
	}
	defer rows.Close()

	var agents []*omniagent.AgentInfo
	for rows.Next() {
		info, err := scanAgent(rows)
		if err != nil {
			logger.Error("ListAgentsByDir: scan failed", "workspaceDir", dir, "err", err)
			return nil, err
		}
		agents = append(agents, info)
	}
	if err := rows.Err(); err != nil {
		logger.Error("ListAgentsByDir: rows iteration failed", "workspaceDir", dir, "err", err)
		return nil, err
	}
	logger.Info("ListAgentsByDir: completed", "workspaceDir", dir, "count", len(agents))
	return agents, nil
}

func (s *sqlStore) GetAgent(id string) (*omniagent.AgentInfo, error) {
	logger.Debug("GetAgent(store): query", "agentID", id)
	row := s.db.QueryRow(
		`SELECT id, name, workspace_dir, memory_dir FROM agents WHERE id = ?`, id,
	)
	var info omniagent.AgentInfo
	var wsDir string
	if err := row.Scan(&info.ID, &info.Name, &wsDir, &info.MemoryDir); err != nil {
		logger.Error("GetAgent(store): query failed", "agentID", id, "err", err)
		return nil, err
	}
	info.WorkspaceDir = sandbox.WorkspaceDir(wsDir)
	logger.Info("GetAgent(store): loaded", "agentID", id, "workspaceDir", wsDir)
	return &info, nil
}

func (s *sqlStore) CreateAgent(agent *omniagent.AgentInfo) error {
	logger.Debug("CreateAgent(store): insert", "agentID", agent.ID, "workspaceDir", agent.WorkspaceDir)
	_, err := s.db.Exec(
		`INSERT INTO agents (id, name, workspace_dir, memory_dir) VALUES (?, ?, ?, ?)`,
		agent.ID, agent.Name, string(agent.WorkspaceDir), agent.MemoryDir,
	)
	if err != nil {
		logger.Error("CreateAgent(store): insert failed", "agentID", agent.ID, "workspaceDir", agent.WorkspaceDir, "err", err)
		return err
	}
	logger.Info("CreateAgent(store): inserted", "agentID", agent.ID, "workspaceDir", agent.WorkspaceDir)
	return err
}

func (s *sqlStore) DeleteAgent(id string) error {
	logger.Debug("DeleteAgent(store): delete", "agentID", id)
	res, err := s.db.Exec(`DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		logger.Error("DeleteAgent(store): delete failed", "agentID", id, "err", err)
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		logger.Error("DeleteAgent(store): rows affected failed", "agentID", id, "err", err)
		return err
	}
	if n == 0 {
		logger.Error("DeleteAgent(store): agent not found", "agentID", id)
		return fmt.Errorf("operator: agent %q not found", id)
	}
	logger.Info("DeleteAgent(store): completed", "agentID", id)
	return nil
}

// --- helpers ---

func scanWorkspace(row *sql.Row) (*operator.TeamInfo, error) {
	var t operator.TeamInfo
	if err := row.Scan(&t.ID, &t.Name, &t.Remote, &t.WorkspaceDir, &t.Agents); err != nil {
		return nil, err
	}
	return &t, nil
}

func scanAgent(rows *sql.Rows) (*omniagent.AgentInfo, error) {
	var info omniagent.AgentInfo
	var wsDir string
	if err := rows.Scan(&info.ID, &info.Name, &wsDir, &info.MemoryDir); err != nil {
		return nil, err
	}
	info.WorkspaceDir = sandbox.WorkspaceDir(wsDir)
	return &info, nil
}
