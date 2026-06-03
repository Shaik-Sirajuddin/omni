package session

import (
	"context"
	"database/sql"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/store/database"
)

// TaskDelivery represents a single in-progress or completed task delivery to an agent.
type TaskDelivery struct {
	TaskID        string `json:"task_id"`
	AgentID       string `json:"agent_id"`
	LastMessageID string `json:"last_message_id"`
	Status        string `json:"status"`
	UpdatedAt     int64  `json:"updated_at"`
}

// TaskDeliveryStore tracks the delivery status of tasks to agents.
type TaskDeliveryStore interface {
	// StartDelivery records or updates a task delivery as in_progress.
	StartDelivery(ctx context.Context, taskID, agentID, lastMessageID string) error
	// CompleteDelivery marks a task delivery as completed.
	CompleteDelivery(ctx context.Context, taskID, agentID string) error
	// GetInProgress returns all in_progress deliveries for an agent.
	GetInProgress(ctx context.Context, agentID string) ([]TaskDelivery, error)
}

type sqlTaskDeliveryStore struct {
	db *sql.DB
}

// NewTaskDeliveryStore returns a TaskDeliveryStore backed by the default DB.
func NewTaskDeliveryStore() (TaskDeliveryStore, error) {
	db, err := database.GetDefaultDB()
	if err != nil {
		return nil, err
	}
	return &sqlTaskDeliveryStore{db: db}, nil
}

// NewTaskDeliveryStoreFromDB returns a TaskDeliveryStore backed by db.
func NewTaskDeliveryStoreFromDB(db *sql.DB) TaskDeliveryStore {
	return &sqlTaskDeliveryStore{db: db}
}

func (s *sqlTaskDeliveryStore) StartDelivery(ctx context.Context, taskID, agentID, lastMessageID string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO task_deliveries (task_id, agent_id, last_message_id, status, updated_at)
		 VALUES (?, ?, ?, 'in_progress', ?)
		 ON CONFLICT(task_id, agent_id) DO UPDATE SET last_message_id = excluded.last_message_id,
		                                              status = 'in_progress',
		                                              updated_at = excluded.updated_at`,
		taskID, agentID, lastMessageID, now,
	)
	return err
}

func (s *sqlTaskDeliveryStore) CompleteDelivery(ctx context.Context, taskID, agentID string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx,
		`UPDATE task_deliveries SET status = 'completed', updated_at = ?
		 WHERE task_id = ? AND agent_id = ?`,
		now, taskID, agentID,
	)
	return err
}

func (s *sqlTaskDeliveryStore) GetInProgress(ctx context.Context, agentID string) ([]TaskDelivery, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id, agent_id, last_message_id, status, updated_at
		 FROM task_deliveries WHERE agent_id = ? AND status = 'in_progress'`,
		agentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deliveries []TaskDelivery
	for rows.Next() {
		var d TaskDelivery
		if err := rows.Scan(&d.TaskID, &d.AgentID, &d.LastMessageID, &d.Status, &d.UpdatedAt); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}
