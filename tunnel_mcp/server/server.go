package server

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Shaik-Sirajuddin/omni/mcp/broadcast"
	"github.com/Shaik-Sirajuddin/omni/mcp/engine"
	httpapi "github.com/Shaik-Sirajuddin/omni/mcp/server/http"
	mcpapi "github.com/Shaik-Sirajuddin/omni/mcp/server/mcp"
	"github.com/Shaik-Sirajuddin/omni/mcp/server/service"
	"github.com/Shaik-Sirajuddin/omni/mcp/store/agents"
	storebroadcast "github.com/Shaik-Sirajuddin/omni/mcp/store/broadcast"
	"github.com/Shaik-Sirajuddin/omni/mcp/store/database"
	"github.com/Shaik-Sirajuddin/omni/mcp/store/message"
	storesession "github.com/Shaik-Sirajuddin/omni/mcp/store/session"
	operatorstore "github.com/Shaik-Sirajuddin/omni/store/operator"
)

const defaultAuthToken = "tunnel-mcp-dev-token"
const serviceVersion = "0.0.2"
const defaultMCPHTTPPath = "/mcp"

type delivery interface {
	MessageArrived(context.Context, string, string)
}

type deliveryRunner interface {
	Run(context.Context) error
}

type hookRegistrar interface {
	RegisterHookRoutes(*http.ServeMux)
}

type Server struct {
	interval     time.Duration
	msgStore     message.MessageStore
	agentStore   agents.AgentStore
	teamStore    service.TeamStore
	broadcastSvc broadcast.Service
	delivery     delivery
	authToken    string
	nowUnixMS    func() int64
	service      *service.Service
}

type Option func(*Server)

func WithMessageStore(msgStore message.MessageStore) Option {
	return func(s *Server) { s.msgStore = msgStore }
}

func WithDelivery(delivery delivery) Option {
	return func(s *Server) { s.delivery = delivery }
}

func WithAgentStore(agentStore agents.AgentStore) Option {
	return func(s *Server) { s.agentStore = agentStore }
}

func WithTeamStore(teamStore service.TeamStore) Option {
	return func(s *Server) { s.teamStore = teamStore }
}

func WithAuthToken(token string) Option {
	return func(s *Server) { s.authToken = token }
}

func WithBroadcastService(service broadcast.Service) Option {
	return func(s *Server) { s.broadcastSvc = service }
}

func WithClock(now func() int64) Option {
	return func(s *Server) { s.nowUnixMS = now }
}

func New(interval time.Duration) *Server {
	return NewWithDelivery(interval, nil)
}

func NewWithDelivery(interval time.Duration, delivery delivery, opts ...Option) *Server {
	srv := &Server{
		interval:  interval,
		delivery:  delivery,
		authToken: envDefault("AXO_LINK_MCP_AUTH_TOKEN", defaultAuthToken),
		nowUnixMS: func() int64 { return time.Now().UnixMilli() },
	}
	for _, opt := range opts {
		opt(srv)
	}
	if srv.msgStore == nil {
		db, err := database.GetDefaultDB()
		if err != nil {
			logger.Error("open mcp message db failed", "err", err)
		} else {
			srv.msgStore = message.New(db)
			if srv.broadcastSvc == nil {
				srv.broadcastSvc = broadcast.New(srv.msgStore, storebroadcast.New(db))
			}
		}
	}
	if srv.agentStore == nil {
		store, err := agents.GetStore()
		if err != nil {
			logger.Error("open agent store failed", "err", err)
		} else {
			srv.agentStore = store
		}
	}
	if srv.teamStore == nil {
		store, err := operatorstore.GetOperatorStore()
		if err != nil {
			logger.Error("open operator store failed", "err", err)
		} else {
			srv.teamStore = store
		}
	}
	if srv.delivery == nil && srv.msgStore != nil {
		var engOpts []engine.Option
		if pss, err := storesession.NewPromptSessionStore(); err != nil {
			logger.Warn("prompt session store init failed, warm-up/active split disabled", "err", err)
		} else {
			engOpts = append(engOpts, engine.WithPromptSessionStore(pss))
		}
		eng := engine.New(srv.msgStore, engOpts...)
		eng.SetReplyService(newReplyService(srv.msgStore, eng.MessageArrived))
		srv.delivery = eng
	}
	srv.service = service.New(srv.msgStore, srv.delivery, srv.agentStore, srv.teamStore, srv.nowUnixMS)
	return srv
}

func (s *Server) Service() *service.Service {
	return s.service
}

func (s *Server) Routes() http.Handler {
	return s.httpHandler().Routes()
}

func (s *Server) MCPHandler() http.Handler {
	return mcpapi.New(s.service, mcpapi.WithServiceVersion(serviceVersion)).MCPHandler()
}

func (s *Server) DaemonHandler(mcpPath string) http.Handler {
	mux := http.NewServeMux()
	s.httpHandler().RegisterRoutes(mux)
	if strings.TrimSpace(mcpPath) == "" {
		mcpPath = defaultMCPHTTPPath
	}
	if !strings.HasPrefix(mcpPath, "/") {
		mcpPath = "/" + mcpPath
	}
	mux.Handle(mcpPath, s.MCPHandler())
	return mux
}

func (s *Server) RunInference(ctx context.Context) error {
	errCh := make(chan error, 1)
	if runner, ok := s.delivery.(deliveryRunner); ok {
		go func() { errCh <- runner.Run(ctx) }()
	}

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		case <-ticker.C:
			logger.Debug("mcp inference tick")
		}
	}
}

func DeliveryModeFromEnv() string {
	return envDefault("AXO_LINK_MCP_DELIVERY_MODE", "async")
}

func (s *Server) httpHandler() *httpapi.Handler {
	opts := []httpapi.Option{
		httpapi.WithAuthToken(s.authToken),
		httpapi.WithServiceVersion(serviceVersion),
	}
	if registrar, ok := s.delivery.(hookRegistrar); ok {
		opts = append(opts, httpapi.WithHookRegistrar(registrar))
	}
	return httpapi.New(s.service, opts...)
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
