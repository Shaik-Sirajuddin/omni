package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type httpClient struct {
	socketPath string
	http       *http.Client
}

func newHTTPClient() *httpClient {
	socketPath := os.Getenv("PTYDAEMON_SOCKET")
	if socketPath == "" {
		socketPath = "/tmp/ptydaemon.sock"
	}
	return &httpClient{
		socketPath: socketPath,
		http: &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

func (c *httpClient) Pipe(agentID, sessionID string, data []byte) error {
	body, err := json.Marshal(struct {
		AgentID   string `json:"agent_id"`
		SessionID string `json:"session_id"`
		Data      []byte `json:"data"`
	}{AgentID: agentID, SessionID: sessionID, Data: data})
	if err != nil {
		return err
	}
	return c.post("http://ptydaemon/pipe", body)
}

func (c *httpClient) Start(sessionID string, command []string, _ []string, _, _ string) error {
	return fmt.Errorf("ptydaemon/clients: http client does not support Start (session %q)", sessionID)
}

func (c *httpClient) Attach(_ context.Context, sessionID string) error {
	return fmt.Errorf("ptydaemon/clients: http client does not support Attach (session %q)", sessionID)
}

func (c *httpClient) Exec(sessionID, input string) error {
	return c.Pipe("", sessionID, []byte(input))
}

func (c *httpClient) Stop(sessionID string) error {
	return fmt.Errorf("ptydaemon/clients: http client does not support Stop (session %q)", sessionID)
}

func (c *httpClient) StopSafe(sessionID string, force bool) error {
	return fmt.Errorf("ptydaemon/clients: http client does not support StopSafe (session %q, force %t)", sessionID, force)
}

func (c *httpClient) Detach(sessionID string) error {
	return fmt.Errorf("ptydaemon/clients: http client does not support Detach (session %q)", sessionID)
}

func (c *httpClient) List(agentID string) ([]*PTYTerminalInfo, error) {
	url := "http://ptydaemon/sessions"
	if agentID != "" {
		url += "?agent_id=" + agentID
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ptydaemon/clients: list %s: %w", c.socketPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ptydaemon/clients: list: %s", resp.Status)
	}
	var all []*PTYTerminalInfo
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return nil, fmt.Errorf("ptydaemon/clients: list decode: %w", err)
	}
	return all, nil
}

func (c *httpClient) Get(agentID, sessionID string) (*PTYTerminalInfo, error) {
	url := fmt.Sprintf("http://ptydaemon/session?agent_id=%s&session_id=%s", agentID, sessionID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ptydaemon/clients: get %s: %w", c.socketPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ptydaemon/clients: get: %s", resp.Status)
	}
	var info PTYTerminalInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("ptydaemon/clients: get decode: %w", err)
	}
	return &info, nil
}

func (c *httpClient) Register(agentID, sessionID, processID string) error {
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(processID) == "" {
		return nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(processID))
	if err != nil {
		return fmt.Errorf("ptydaemon/clients: register: invalid pid %q: %w", processID, err)
	}
	body, err := json.Marshal(struct {
		AgentID   string `json:"agent_id"`
		SessionID string `json:"session_id"`
		PID       int    `json:"pid"`
		SubmitKey string `json:"submit_key"`
	}{AgentID: agentID, SessionID: sessionID, PID: pid})
	if err != nil {
		return err
	}
	return c.post("http://ptydaemon/adopt", body)
}

func (c *httpClient) ListAttached(sessionID string) ([]AttachedProcess, error) {
	return nil, fmt.Errorf("ptydaemon/clients: http client does not support ListAttached (session %q)", sessionID)
}

func (c *httpClient) MetaAttached(sessionID string) (int, error) {
	return 0, fmt.Errorf("ptydaemon/clients: http client does not support MetaAttached (session %q)", sessionID)
}

func (c *httpClient) post(url string, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ptydaemon/clients: POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ptydaemon/clients: POST %s: %s", url, resp.Status)
	}
	return nil
}
