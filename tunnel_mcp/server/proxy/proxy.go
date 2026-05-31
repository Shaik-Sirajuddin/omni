package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/mcp/server/service"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/agents"
	pkglog "github.com/Shaik-Sirajuddin/memory/pkg/log"
)

var logger = pkglog.NewLogger("component", "proxy-server")

type ProxyServer struct {
	client    *http.Client
	baseURL   string
	authToken string
}

type Option func(*ProxyServer)

func WithAuthToken(token string) Option {
	return func(p *ProxyServer) { p.authToken = token }
}

// New creates a ProxyServer that forwards calls to the daemon's service HTTP endpoint.
// When serviceHTTPBind is "unix", connects via unix socket at socketPath.
// Otherwise connects via TCP to serviceAddr.
func New(serviceAddr, socketPath, serviceHTTPBind string, opts ...Option) *ProxyServer {
	var (
		transport *http.Transport
		baseURL   string
	)
	if serviceHTTPBind == "unix" {
		transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		}
		baseURL = "http://localhost"
	} else {
		transport = &http.Transport{}
		host, port, err := net.SplitHostPort(serviceAddr)
		if err == nil {
			switch host {
			case "", "0.0.0.0", "::", "[::]":
				host = "127.0.0.1"
			}
			baseURL = "http://" + net.JoinHostPort(host, port)
		} else if strings.HasPrefix(serviceAddr, ":") {
			baseURL = "http://127.0.0.1" + serviceAddr
		} else {
			baseURL = "http://" + strings.TrimRight(serviceAddr, "/")
		}
	}
	p := &ProxyServer{
		client:  &http.Client{Transport: transport},
		baseURL: baseURL,
	}
	for _, opt := range opts {
		opt(p)
	}
	logger.Info("proxy server initialized", "base_url", p.baseURL, "bind", serviceHTTPBind)
	return p
}

func (p *ProxyServer) SendMessage(ctx context.Context, sender service.SenderSpec, payload service.PayloadMessage) (service.SendMessageResponse, error) {
	logger.Debug("proxy send_message", "sender_id", sender.ID, "to_id", payload.To.ID, "to_name", payload.To.Name)
	var resp service.SendMessageResponse
	err := p.post(ctx, sender, "/send-message", service.SendMessageRequest{Payload: payload}, &resp)
	return resp, err
}

func (p *ProxyServer) SendGroupMessage(ctx context.Context, sender service.SenderSpec, payloads []service.PayloadMessage) (service.SendGroupMessageResponse, error) {
	logger.Debug("proxy send_group_message", "sender_id", sender.ID, "count", len(payloads))
	var resp service.SendGroupMessageResponse
	err := p.post(ctx, sender, "/send-group-message", service.SendGroupMessageRequest{Messages: payloads}, &resp)
	return resp, err
}

func (p *ProxyServer) QueryResult(ctx context.Context, sender service.SenderSpec, item service.QueryResultItem) (service.QueryResultResponse, error) {
	logger.Debug("proxy query_result", "sender_id", sender.ID, "message_id", item.MessageID)
	var resp service.QueryResultResponse
	err := p.post(ctx, sender, "/query-result", service.QueryResultRequest{Item: item}, &resp)
	return resp, err
}

func (p *ProxyServer) QueryResultBatch(ctx context.Context, sender service.SenderSpec, items []service.QueryResultItem) (service.QueryResultBatchResponse, error) {
	logger.Debug("proxy query_result_batch", "sender_id", sender.ID, "count", len(items))
	var resp service.QueryResultBatchResponse
	err := p.post(ctx, sender, "/query-result-batch", service.QueryResultBatchRequest{Items: items}, &resp)
	return resp, err
}

func (p *ProxyServer) GetMessage(ctx context.Context, id string) (*service.MessageResponse, error) {
	logger.Debug("proxy get_message", "id", id)
	var resp service.MessageResponse
	err := p.get(ctx, service.SenderSpec{}, "/get-message", url.Values{"id": {id}}, &resp)
	return &resp, err
}

func (p *ProxyServer) ListMessages(ctx context.Context, sender service.SenderSpec, req service.ListMessagesRequest) ([]*service.MessageResponse, error) {
	logger.Debug("proxy list_messages", "sender_id", sender.ID, "from", req.From, "to", req.To, "group_id", req.GroupID)
	q := url.Values{}
	if req.ID != "" {
		q.Set("id", req.ID)
	}
	if req.GroupID != "" {
		q.Set("group_id", req.GroupID)
	}
	if req.From != "" {
		q.Set("from", req.From)
	}
	if req.To != "" {
		q.Set("to", req.To)
	}
	if len(req.IDs) > 0 {
		b, _ := json.Marshal(req.IDs)
		q.Set("ids_json", string(b))
	}
	q.Set("offset", strconv.Itoa(req.Page.Offset))
	q.Set("limit", strconv.Itoa(req.Page.Limit))
	var resp []*service.MessageResponse
	err := p.get(ctx, sender, "/list-messages", q, &resp)
	return resp, err
}

func (p *ProxyServer) ListAgents(ctx context.Context, sender service.SenderSpec) (*service.ListAgentsResponse, error) {
	logger.Debug("proxy list_agents", "workspace", sender.Workspace)
	var list []*agents.AgentInfo
	if err := p.get(ctx, sender, "/list-agents", nil, &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []*agents.AgentInfo{}
	}
	return &service.ListAgentsResponse{Agents: list, Count: len(list)}, nil
}

func (p *ProxyServer) ListTeams(ctx context.Context, sender service.SenderSpec) (*service.ListTeamsResponse, error) {
	logger.Debug("proxy list_teams")
	var resp service.ListTeamsResponse
	err := p.get(ctx, sender, "/list-teams", nil, &resp)
	return &resp, err
}

func (p *ProxyServer) InterruptAgent(ctx context.Context, sender service.SenderSpec, agentID string) error {
	logger.Debug("proxy agent_interrupt", "agent_id", agentID)
	return p.post(ctx, sender, "/agent-interrupt", service.AgentControlRequest{AgentID: agentID}, nil)
}

func (p *ProxyServer) ResumeAgent(ctx context.Context, sender service.SenderSpec, agentID string) error {
	logger.Debug("proxy agent_resume", "agent_id", agentID)
	return p.post(ctx, sender, "/agent-resume", service.AgentControlRequest{AgentID: agentID}, nil)
}

func (p *ProxyServer) CheckStatus(ctx context.Context, sender service.SenderSpec, agentID string) (*service.AgentStatusResponse, error) {
	logger.Debug("proxy check_status", "agent_id", agentID)
	var resp service.AgentStatusResponse
	err := p.get(ctx, sender, "/check-status", url.Values{"agent_id": {agentID}}, &resp)
	return &resp, err
}

func (p *ProxyServer) post(ctx context.Context, sender service.SenderSpec, path string, body any, out any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return fmt.Errorf("proxy marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, &buf)
	if err != nil {
		return fmt.Errorf("proxy request build: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	p.setSenderHeaders(req, sender)
	return p.do(req, out)
}

func (p *ProxyServer) get(ctx context.Context, sender service.SenderSpec, path string, q url.Values, out any) error {
	u := p.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("proxy request build: %w", err)
	}
	p.setSenderHeaders(req, sender)
	return p.do(req, out)
}

func (p *ProxyServer) setSenderHeaders(req *http.Request, sender service.SenderSpec) {
	if p.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.authToken)
	}
	if sender.ID != "" {
		req.Header.Set("X-SENDER-ID", sender.ID)
	}
	if sender.Kind != "" {
		req.Header.Set("X-SENDER-TYPE", string(sender.Kind))
	}
	if sender.Workspace != "" {
		req.Header.Set("X-AGENT-WORKSPACE", sender.Workspace)
	}
}

func (p *ProxyServer) do(req *http.Request, out any) error {
	resp, err := p.client.Do(req)
	if err != nil {
		logger.Error("proxy http call failed", "path", req.URL.Path, "err", err)
		return fmt.Errorf("proxy call %s: %w", req.URL.Path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("proxy read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp service.ErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return service.HTTPError(resp.StatusCode, errResp.Error)
		}
		return service.HTTPError(resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}
