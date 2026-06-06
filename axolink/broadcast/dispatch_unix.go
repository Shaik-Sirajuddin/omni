package broadcast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type UnixDispatcher struct {
	timeout time.Duration
}

func NewUnixDispatcher(timeout time.Duration) *UnixDispatcher {
	logger.Debug("broadcast unix dispatcher initializing", "timeout", timeout)
	return &UnixDispatcher{timeout: timeout}
}

func (d *UnixDispatcher) Dispatch(ctx context.Context, entry MCPClientEntry, payload CallbackPayload) error {
	logger.Debug("broadcast unix dispatch preparing",
		"server_id", entry.ServerID,
		"message_id", payload.MessageID,
		"endpoint", entry.Endpoint,
		"auth_ref_present", entry.AuthenticationRef != "",
	)
	socketPath, requestPath, err := parseUnixEndpoint(entry.Endpoint)
	if err != nil {
		logger.Error("broadcast unix dispatch endpoint parse failed", "err", err, "server_id", entry.ServerID, "endpoint", entry.Endpoint)
		return err
	}
	logger.Debug("broadcast unix dispatch endpoint parsed", "server_id", entry.ServerID, "message_id", payload.MessageID, "socket_path", socketPath, "request_path", requestPath)

	body, err := json.Marshal(payload)
	if err != nil {
		logger.Error("broadcast unix dispatch marshal failed", "err", err, "server_id", entry.ServerID, "message_id", payload.MessageID)
		return err
	}
	logger.Debug("broadcast unix dispatch payload encoded", "server_id", entry.ServerID, "message_id", payload.MessageID, "bytes", len(body))

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			logger.Debug("broadcast unix dispatch dialing", "server_id", entry.ServerID, "socket_path", socketPath)
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport, Timeout: d.timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+requestPath, bytes.NewReader(body))
	if err != nil {
		logger.Error("broadcast unix dispatch request build failed", "err", err, "server_id", entry.ServerID, "request_path", requestPath)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if entry.AuthenticationRef != "" {
		req.Header.Set("Authorization", "Bearer "+entry.AuthenticationRef)
	}

	logger.Debug("broadcast unix dispatch sending", "server_id", entry.ServerID, "message_id", payload.MessageID, "socket_path", socketPath, "request_path", requestPath)
	resp, err := client.Do(req)
	if err != nil {
		logger.Error("broadcast unix dispatch request failed", "err", err, "server_id", entry.ServerID, "message_id", payload.MessageID, "socket_path", socketPath)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		err := fmt.Errorf("unix callback failed: status=%d body=%q", resp.StatusCode, string(data))
		logger.Error("broadcast unix dispatch response failed", "err", err, "server_id", entry.ServerID, "message_id", payload.MessageID, "status", resp.StatusCode)
		return err
	}
	logger.Debug("broadcast unix dispatch response received", "server_id", entry.ServerID, "message_id", payload.MessageID, "status", resp.StatusCode)
	return nil
}

func parseUnixEndpoint(endpoint string) (string, string, error) {
	logger.Debug("broadcast unix endpoint parsing", "endpoint", endpoint)
	const prefix = "unix://"
	if !strings.HasPrefix(endpoint, prefix) {
		return "", "", fmt.Errorf("unix endpoint must start with %q", prefix)
	}
	rest := strings.TrimPrefix(endpoint, prefix)
	separator := strings.LastIndex(rest, ":/")
	if separator < 0 {
		return "", "", fmt.Errorf("unix endpoint must be unix://<socket>:/<path>")
	}
	socketPath := rest[:separator]
	requestPath := rest[separator+1:]
	if socketPath == "" || requestPath == "" {
		return "", "", fmt.Errorf("unix endpoint requires socket and path")
	}
	return socketPath, requestPath, nil
}
