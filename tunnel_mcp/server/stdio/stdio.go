package stdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/Shaik-Sirajuddin/memory/mcp/server/service"
	pkglog "github.com/Shaik-Sirajuddin/memory/pkg/log"
)

const defaultMCPHTTPPath = "/mcp"
const defaultMCPHTTPAddr = "http://127.0.0.1:18062"

var logger = pkglog.NewLogger("component", "stdio-bridge")

type StdioServer struct {
	endpoint   string
	socketPath string
	headers    map[string]string
	client     *http.Client
}

func NewStdio(_ time.Duration) *StdioServer {
	endpoint := mcpHTTPEndpoint(
		envDefault("AXO_LINK_MCP_ADDR", defaultMCPHTTPAddr),
		envDefault("AXO_LINK_MCP_HTTP_PATH", defaultMCPHTTPPath),
	)
	logger.Info("stdio bridge configured", "endpoint", endpoint)
	return NewStdioBridge(endpoint, "", bridgeHeadersFromEnv())
}

func NewStdioBridge(endpoint, socketPath string, headers map[string]string) *StdioServer {
	transport := &http.Transport{}
	if socketPath != "" {
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		}
	}
	return &StdioServer{
		endpoint:   endpoint,
		socketPath: socketPath,
		headers:    headers,
		client:     &http.Client{Transport: transport},
	}
}

func (s *StdioServer) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	logger.Info("stdio bridge serving", "endpoint", s.endpoint, "socket_path", s.socketPath)
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			logger.Info("stdio bridge stopping", "reason", "context canceled")
			return ctx.Err()
		default:
		}
		payload := scanner.Bytes()
		logger.Debug("stdio bridge frame received", "bytes", len(payload), "endpoint", s.endpoint, "socket_path", s.socketPath, "payload", string(payload))
		resp, err := s.Forward(ctx, payload)
		if err != nil {
			logger.Error("stdio bridge forward failed", "err", err, "bytes", len(payload), "endpoint", s.endpoint, "socket_path", s.socketPath)
			resp = encodeBridgeError(err)
		} else {
			logger.Debug("stdio bridge frame forwarded", "request_bytes", len(payload), "response_bytes", len(resp), "endpoint", s.endpoint)
		}
		if _, err := out.Write(resp); err != nil {
			logger.Error("stdio bridge write response failed", "err", err)
			return err
		}
		if _, err := out.Write([]byte("\n")); err != nil {
			logger.Error("stdio bridge write newline failed", "err", err)
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		logger.Error("stdio bridge scanner failed", "err", err)
		return err
	}
	logger.Info("stdio bridge stopped", "reason", "input closed")
	return nil
}

func (s *StdioServer) Forward(ctx context.Context, payload []byte) ([]byte, error) {
	if strings.TrimSpace(s.endpoint) == "" {
		err := fmt.Errorf("AXO_LINK_MCP streamable HTTP endpoint is required")
		logger.Error("stdio bridge endpoint missing", "err", err)
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		logger.Error("stdio bridge request build failed", "err", err, "endpoint", s.endpoint)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for key, value := range s.headers {
		if value != "" {
			req.Header.Set(key, value)
		}
	}
	logger.Debug("stdio bridge forwarding request", "endpoint", s.endpoint, "socket_path", s.socketPath, "bytes", len(payload), "sender_id", req.Header.Get("X-SENDER-ID"), "sender_type", req.Header.Get("X-SENDER-TYPE"), "workspace", req.Header.Get("X-AGENT-WORKSPACE"))
	resp, err := s.client.Do(req)
	if err != nil {
		if s.socketPath != "" {
			wrapped := fmt.Errorf("stdio bridge could not reach MCP server at unix socket %s: %w", s.socketPath, err)
			logger.Error("stdio bridge request failed", "err", wrapped, "endpoint", s.endpoint, "socket_path", s.socketPath)
			return nil, wrapped
		}
		logger.Error("stdio bridge request failed", "err", err, "endpoint", s.endpoint)
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error("stdio bridge response read failed", "err", err, "endpoint", s.endpoint, "status", resp.StatusCode)
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("MCP server returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
		logger.Error("stdio bridge response failed", "err", err, "endpoint", s.endpoint, "status", resp.StatusCode, "response_bytes", len(body))
		return nil, err
	}
	logger.Debug("stdio bridge response received", "endpoint", s.endpoint, "status", resp.StatusCode, "response_bytes", len(body))
	return body, nil
}

func (s *StdioServer) SetClient(client *http.Client) {
	s.client = client
}

func bridgeHeadersFromEnv() map[string]string {
	token := os.Getenv("AXO_LINK_MCP_AUTH_TOKEN")
	headers := map[string]string{
		"X-SENDER-ID":       os.Getenv("AXO_LINK_MCP_SENDER_ID"),
		"X-SENDER-TYPE":     os.Getenv("AXO_LINK_MCP_SENDER_TYPE"),
		"X-AGENT-WORKSPACE": os.Getenv("AXO_LINK_MCP_AGENT_WORKSPACE"),
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return headers
}

func encodeBridgeError(err error) []byte {
	body, _ := json.Marshal(service.ErrorResponse{Error: err.Error()})
	return body
}

func defaultUnixSocketPath() string {
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

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envDefaultAny(fallback string, keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return fallback
}

func mcpHTTPEndpoint(addr, path string) string {
	if strings.TrimSpace(path) == "" {
		path = defaultMCPHTTPPath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	addr = strings.TrimRight(strings.TrimSpace(addr), "/")
	if addr == "" {
		addr = defaultMCPHTTPAddr
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr + path
	}
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr + path
	}
	return "http://" + addr + path
}
