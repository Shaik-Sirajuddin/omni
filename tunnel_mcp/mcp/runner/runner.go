package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/server"
	mcpapi "github.com/Shaik-Sirajuddin/memory/mcp/server/mcp"
	"github.com/Shaik-Sirajuddin/memory/mcp/server/proxy"
	pkglog "github.com/Shaik-Sirajuddin/memory/pkg/log"
)

const (
	TransportStreamableHTTP = "streamable_http"
	TransportStdio          = "stdio"

	ServiceHTTPBindUnix     = "unix"
	ServiceHTTPBindTCP      = "tcp"
	ServiceHTTPBindDisabled = "disabled"

	DefaultHTTPPath        = "/mcp"
	DefaultAddr            = ":18062"
	DefaultServiceAddr     = ":18061"
	DefaultInterval        = time.Minute
	DefaultStdioInterval   = 10 * time.Second
	DefaultShutdownTimeout = 10 * time.Second
)

var logger = pkglog.NewLogger("component", "mcp-runner")

type Config struct {
	Transport         string
	Addr              string
	Interval          time.Duration
	ServiceHTTPBind   string
	ServiceAddr       string
	ServiceUnixSocket string
	HTTPPath          string
	AuthToken         string
	DBPath            string
	DeliveryMode      string
	SenderID          string
	SenderType        string
	AgentWorkspace    string
	ShutdownTimeout   time.Duration
	Stdin             io.Reader
	Stdout            io.Writer
}

func DefaultConfig() Config {
	return Config{
		Transport:         envDefault("AXO_LINK_MCP_TRANSPORT", TransportStreamableHTTP),
		Addr:              defaultMCPAddr(),
		Interval:          DefaultInterval,
		ServiceHTTPBind:   envDefault("AXO_LINK_SERVICE_HTTP_BIND", ServiceHTTPBindUnix),
		ServiceAddr:       envDefault("AXO_LINK_SERVICE_ADDR", DefaultServiceAddr),
		ServiceUnixSocket: envDefault("AXO_LINK_SERVICE_UNIX_SOCKET", DefaultServiceUnixSocketPath()),
		HTTPPath:          envDefault("AXO_LINK_MCP_HTTP_PATH", DefaultHTTPPath),
		AuthToken:         envDefault("AXO_LINK_MCP_AUTH_TOKEN", "tunnel-mcp-dev-token"),
		DBPath:            os.Getenv("AXO_LINK_MCP_DB_PATH"),
		DeliveryMode:      envDefault("AXO_LINK_MCP_DELIVERY_MODE", "async"),
		SenderID:          os.Getenv("AXO_LINK_MCP_SENDER_ID"),
		SenderType:        os.Getenv("AXO_LINK_MCP_SENDER_TYPE"),
		AgentWorkspace:    os.Getenv("AXO_LINK_MCP_AGENT_WORKSPACE"),
		ShutdownTimeout:   DefaultShutdownTimeout,
		Stdin:             os.Stdin,
		Stdout:            os.Stdout,
	}
}

func Run(ctx context.Context, cfg Config) error {
	cfg = normalize(cfg)
	if cfg.Transport == TransportStdio {
		logger.Info("mcp stdio server starting", "sender_id", cfg.SenderID, "sender_type", cfg.SenderType, "service_bind", cfg.ServiceHTTPBind)
		ps := proxy.New(cfg.ServiceAddr, cfg.ServiceUnixSocket, cfg.ServiceHTTPBind,
			proxy.WithAuthToken(cfg.AuthToken),
		)
		return mcpapi.NewStdioHandler(ps,
			mcpapi.WithStdioSenderInfo(mcpapi.SenderInfo{
				ID:        cfg.SenderID,
				Kind:      cfg.SenderType,
				Workspace: cfg.AgentWorkspace,
			}),
		).StdioServer().Listen(ctx, cfg.Stdin, cfg.Stdout)
	}
	if cfg.Transport != TransportStreamableHTTP {
		return fmt.Errorf("unsupported MCP transport %q", cfg.Transport)
	}
	if !isServiceHTTPBind(cfg.ServiceHTTPBind) {
		return fmt.Errorf("unsupported service HTTP bind %q", cfg.ServiceHTTPBind)
	}
	if cfg.ServiceHTTPBind != ServiceHTTPBindDisabled {
		if err := provisionDefaultHooks(cfg); err != nil {
			return err
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	srv := server.NewWithDelivery(cfg.Interval, nil,
		server.WithAuthToken(cfg.AuthToken),
	)

	mcpHTTPServer := &http.Server{
		Addr:              listenAddr(cfg.Addr),
		Handler:           srv.MCPHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serviceHTTPServer := &http.Server{
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 3)
	go func() {
		logger.Info("mcp streamable http server starting", "addr", mcpHTTPServer.Addr, "path", cfg.HTTPPath)
		errCh <- mcpHTTPServer.ListenAndServe()
	}()

	if cfg.ServiceHTTPBind != ServiceHTTPBindDisabled {
		go func() {
			switch cfg.ServiceHTTPBind {
			case ServiceHTTPBindTCP:
				serviceHTTPServer.Addr = cfg.ServiceAddr
				logger.Info("service http tcp server starting", "addr", cfg.ServiceAddr)
				errCh <- serviceHTTPServer.ListenAndServe()
			case ServiceHTTPBindUnix:
				logger.Info("service http unix server starting", "socket", cfg.ServiceUnixSocket)
				errCh <- serveUnix(serviceHTTPServer, cfg.ServiceUnixSocket)
			}
		}()
	}

	go func() {
		errCh <- srv.RunInference(runCtx)
	}()

	servers := []*http.Server{mcpHTTPServer, serviceHTTPServer}
	select {
	case <-runCtx.Done():
		logger.Info("mcp server stopping", "err", runCtx.Err())
		return shutdownServers(servers, cfg.ShutdownTimeout, runCtx.Err())
	case err := <-errCh:
		if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, context.Canceled) {
			return nil
		}
		cancel()
		if shutdownErr := shutdownServers(servers, cfg.ShutdownTimeout, err); shutdownErr != nil {
			return shutdownErr
		}
		return err
	}
}

func normalize(cfg Config) Config {
	defaults := DefaultConfig()
	if cfg.Transport == "" {
		cfg.Transport = defaults.Transport
	}
	if cfg.Addr == "" {
		cfg.Addr = defaults.Addr
	}
	if cfg.Interval <= 0 {
		if cfg.Transport == TransportStdio {
			cfg.Interval = DefaultStdioInterval
		} else {
			cfg.Interval = DefaultInterval
		}
	}
	if cfg.ServiceHTTPBind == "" {
		cfg.ServiceHTTPBind = defaults.ServiceHTTPBind
	}
	if cfg.ServiceAddr == "" {
		cfg.ServiceAddr = defaults.ServiceAddr
	}
	if cfg.ServiceUnixSocket == "" {
		cfg.ServiceUnixSocket = defaults.ServiceUnixSocket
	}
	if cfg.HTTPPath == "" {
		cfg.HTTPPath = defaults.HTTPPath
	}
	if cfg.AuthToken == "" {
		cfg.AuthToken = defaults.AuthToken
	}
	if cfg.DeliveryMode == "" {
		cfg.DeliveryMode = defaults.DeliveryMode
	}
	if cfg.SenderID == "" {
		cfg.SenderID = defaults.SenderID
	}
	if cfg.SenderType == "" {
		cfg.SenderType = defaults.SenderType
	}
	if cfg.AgentWorkspace == "" {
		cfg.AgentWorkspace = defaults.AgentWorkspace
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = DefaultShutdownTimeout
	}
	if cfg.Stdin == nil {
		cfg.Stdin = os.Stdin
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	return cfg
}

func isServiceHTTPBind(bind string) bool {
	return bind == ServiceHTTPBindUnix || bind == ServiceHTTPBindTCP || bind == ServiceHTTPBindDisabled
}

func bridgeHeaders(cfg Config) map[string]string {
	headers := map[string]string{
		"X-SENDER-ID":       cfg.SenderID,
		"X-SENDER-TYPE":     cfg.SenderType,
		"X-AGENT-WORKSPACE": cfg.AgentWorkspace,
	}
	if cfg.AuthToken != "" {
		headers["Authorization"] = "Bearer " + cfg.AuthToken
	}
	return headers
}

func shutdownServers(servers []*http.Server, timeout time.Duration, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var shutdownErr error
	for _, httpServer := range servers {
		if httpServer == nil {
			continue
		}
		if err := httpServer.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	if shutdownErr != nil {
		return errors.Join(cause, shutdownErr)
	}
	return cause
}

func serveUnix(httpServer *http.Server, socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return err
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	return httpServer.Serve(listener)
}

func mcpHTTPEndpoint(addr, path string) string {
	if strings.TrimSpace(path) == "" {
		path = DefaultHTTPPath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	addr = strings.TrimRight(strings.TrimSpace(addr), "/")
	if addr == "" {
		addr = DefaultAddr
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr + path
	}
	return tcpURL(addr, path)
}

func tcpURL(addr, path string) string {
	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		switch host {
		case "", "0.0.0.0", "::", "[::]":
			host = "127.0.0.1"
		}
		return "http://" + net.JoinHostPort(host, port) + path
	}
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr + path
	}
	return "http://" + strings.TrimRight(addr, "/") + path
}

func listenAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		parsed := strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
		return strings.TrimPrefix(strings.Split(parsed, "/")[0], "127.0.0.1")
	}
	return addr
}

func defaultMCPAddr() string {
	return envDefault("AXO_LINK_MCP_ADDR", DefaultAddr)
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func DefaultServiceUnixSocketPath() string {
	return filepath.Join("/run", "omni-"+currentUsername(), "service.sock")
}

func currentUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return "omni"
}
