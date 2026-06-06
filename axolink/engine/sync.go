package engine

import (
	"context"
	"encoding/json"
	"net"
	"os"
)

const DefaultSyncSocketPath = "/tmp/mcp-engine-sync.sock"

// SessionSyncPayload is the JSON body sent by omni-server on each /session-sync call.
type SessionSyncPayload struct {
	Session      string       `json:"session"`
	SessionUsage SessionUsage `json:"session_usage"`
}

// SyncServer listens on a unix socket for session-sync callbacks from omni-server
// and keeps the engine's in-memory state current.
type SyncServer struct {
	socketPath string
	state      *EngineState
	onSync     func(agentID string, usage SessionUsage)
}

func newSyncServer(socketPath string, state *EngineState, onSync func(string, SessionUsage)) *SyncServer {
	return &SyncServer{socketPath: socketPath, state: state, onSync: onSync}
}

func (s *SyncServer) Run(ctx context.Context) error {
	_ = os.Remove(s.socketPath)

	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		logger.Error("sync server listen failed", "path", s.socketPath, "err", err)
		return err
	}
	defer l.Close()
	logger.Info("sync server listening", "path", s.socketPath)

	go func() {
		<-ctx.Done()
		l.Close()
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			logger.Error("sync server accept failed", "err", err)
			continue
		}
		go s.handle(conn)
	}
}

func (s *SyncServer) handle(conn net.Conn) {
	defer conn.Close()

	var payload SessionSyncPayload
	if err := json.NewDecoder(conn).Decode(&payload); err != nil {
		logger.Error("sync server decode failed", "err", err)
		return
	}

	logger.Debug("session sync received",
		"session", payload.Session,
		"consumed_percent", payload.SessionUsage.ConsumedPercent,
	)

	agentState, ok := s.state.GetAgent(payload.Session)
	if !ok {
		agentState = AgentState{Status: AgentStatusRunning}
	}
	agentState.SessionUsage = payload.SessionUsage
	s.state.SetAgent(payload.Session, agentState)

	if s.onSync != nil {
		s.onSync(payload.Session, payload.SessionUsage)
	}
}
