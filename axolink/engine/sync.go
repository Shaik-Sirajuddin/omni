package engine

import (
	"context"
	"encoding/json"
	"net"
	"os"

	"github.com/Shaik-Sirajuddin/memory/mcp/pkg/enginesync"
	"github.com/Shaik-Sirajuddin/memory/pkg/sockpath"
)

// DefaultSyncSocketPath resolves the engine sync socket path via sockpath
// (honouring OMNI_ENGINE_SYNC_SOCKET). The operator dials the same path through
// the enginesync client package.
func DefaultSyncSocketPath() string { return sockpath.EngineSync() }

// SyncServer listens on a unix socket for engine-sync callbacks from the operator
// and delegates them to the engine, which keeps its in-memory state current
// (hydrating from the store as needed). The server itself holds no state.
type SyncServer struct {
	socketPath string
	onSync     func(agentID string, usage SessionUsage)
	onResume   func(agentID string)
}

func newSyncServer(socketPath string, onSync func(string, SessionUsage), onResume func(string)) *SyncServer {
	return &SyncServer{socketPath: socketPath, onSync: onSync, onResume: onResume}
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

	var payload enginesync.Payload
	if err := json.NewDecoder(conn).Decode(&payload); err != nil {
		logger.Error("sync server decode failed", "err", err)
		return
	}

	if payload.Session == "" {
		logger.Warn("engine sync: empty session, ignoring", "kind", payload.Kind)
		return
	}

	switch payload.Kind {
	case enginesync.KindResume:
		// Resume and create both hydrate/sync in-memory state from the store via the
		// engine's onResume handler; neither retriggers delivery (no executeLoop).
		logger.Debug("engine sync: resume received", "session", payload.Session)
		if s.onResume != nil {
			s.onResume(payload.Session)
		}
	case enginesync.KindSessionSync, "":
		logger.Debug("engine sync: session sync received",
			"session", payload.Session,
			"consumed_percent", payload.SessionUsage.ConsumedPercent,
		)

		// Map the wire payload onto the engine's internal SessionUsage. The named
		// types are structurally identical, so the nested conversion is exact.
		usage := SessionUsage{
			Consumed:        ConsumedUsage(payload.SessionUsage.Consumed),
			Max:             payload.SessionUsage.Max,
			ConsumedPercent: payload.SessionUsage.ConsumedPercent,
		}

		if s.onSync != nil {
			s.onSync(payload.Session, usage)
		}
	default:
		logger.Warn("engine sync: unknown payload kind, ignoring", "kind", payload.Kind, "session", payload.Session)
	}
}
