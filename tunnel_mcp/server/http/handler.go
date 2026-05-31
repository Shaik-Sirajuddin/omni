package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/mcp/server/service"
	"github.com/Shaik-Sirajuddin/memory/mcp/store/message"
)

type hookRegistrar interface {
	RegisterHookRoutes(*http.ServeMux)
}

type Handler struct {
	service        *service.Service
	authToken      string
	serviceVersion string
	hooks          hookRegistrar
}

type Option func(*Handler)

func WithAuthToken(token string) Option {
	return func(h *Handler) { h.authToken = token }
}

func WithServiceVersion(version string) Option {
	return func(h *Handler) { h.serviceVersion = version }
}

func WithHookRegistrar(registrar hookRegistrar) Option {
	return func(h *Handler) { h.hooks = registrar }
}

func New(svc *service.Service, opts ...Option) *Handler {
	h := &Handler{service: svc, serviceVersion: "0.0.2"}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.handleHealth)
	mux.HandleFunc("/send-message", h.handleSendMessage)
	mux.HandleFunc("/send-group-message", h.handleSendGroupMessage)
	mux.HandleFunc("/get-message", h.handleGetMessage)
	mux.HandleFunc("/list-messages", h.handleListMessages)
	mux.HandleFunc("/list", h.handleList)
	mux.HandleFunc("/list-agents", h.handleListAgents)
	mux.HandleFunc("/list-teams", h.handleListTeams)
	mux.HandleFunc("/message", h.handleMessage)
	mux.HandleFunc("/query-result", h.handleQueryResult)
	mux.HandleFunc("/query-result-batch", h.handleQueryResultBatch)
	mux.HandleFunc("/agent-interrupt", h.handleAgentInterrupt)
	mux.HandleFunc("/agent-resume", h.handleAgentResume)
	mux.HandleFunc("/check-status", h.handleCheckStatus)
	if h.hooks != nil {
		h.hooks.RegisterHookRoutes(mux)
	}
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, service.HealthResponse{
		Status:  "ok",
		Service: "tunnel-mcp",
		Version: h.serviceVersion,
	})
}

func (h *Handler) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	sender, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req service.SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	resp, err := h.service.SendMessage(r.Context(), sender, req.Payload)
	if err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (h *Handler) handleSendGroupMessage(w http.ResponseWriter, r *http.Request) {
	sender, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req service.SendGroupMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	resp, err := h.service.SendGroupMessage(r.Context(), sender, req.Messages)
	if err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (h *Handler) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	msg, err := h.service.GetMessage(r.Context(), r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

func (h *Handler) handleListMessages(w http.ResponseWriter, r *http.Request) {
	sender, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ids, err := idsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	msgs, err := h.service.ListMessages(r.Context(), sender, service.ListMessagesRequest{
		ID:      strings.TrimSpace(r.URL.Query().Get("id")),
		IDs:     ids,
		GroupID: strings.TrimSpace(r.URL.Query().Get("group_id")),
		From:    strings.TrimSpace(r.URL.Query().Get("from")),
		To:      strings.TrimSpace(r.URL.Query().Get("to")),
		Page:    parsePage(r),
	})
	if err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	msgs, err := h.service.List(r.Context(), service.ListRequest{
		Filter: strings.TrimSpace(r.URL.Query().Get("filter")),
		Team:   strings.TrimSpace(r.URL.Query().Get("team")),
		Page:   parsePage(r),
	})
	if err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (h *Handler) handleListAgents(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := h.service.ListAgents(workspaceFromRequest(r))
	if err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) handleListTeams(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	resp, err := h.service.ListTeams()
	if err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleMessage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	resp, err := h.service.DeleteMessage(r.Context(), strings.TrimSpace(r.URL.Query().Get("id")))
	if err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleQueryResult(w http.ResponseWriter, r *http.Request) {
	sender, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req service.QueryResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	resp, err := h.service.QueryResult(r.Context(), sender, req.Item)
	if err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (h *Handler) handleQueryResultBatch(w http.ResponseWriter, r *http.Request) {
	sender, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req service.QueryResultBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	resp, err := h.service.QueryResultBatch(r.Context(), sender, req.Items)
	if err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (h *Handler) handleAgentInterrupt(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req service.AgentControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.service.InterruptAgent(req.AgentID); err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "interrupted"})
}

func (h *Handler) handleAgentResume(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req service.AgentControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.service.ResumeAgent(r.Context(), req.AgentID); err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

func (h *Handler) handleCheckStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	resp, err := h.service.CheckStatus(r.Context(), r.URL.Query().Get("agent_id"))
	if err != nil {
		writeError(w, service.StatusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (service.SenderSpec, bool) {
	if h.authToken != "" && r.Header.Get("Authorization") != "Bearer "+h.authToken {
		writeError(w, http.StatusUnauthorized, "invalid bearer token")
		return service.SenderSpec{}, false
	}
	id := strings.TrimSpace(r.Header.Get("X-SENDER-ID"))
	if id == "" {
		writeError(w, http.StatusUnauthorized, "X-SENDER-ID is required")
		return service.SenderSpec{}, false
	}
	kind, err := service.ParseBySpec(r.Header.Get("X-SENDER-TYPE"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return service.SenderSpec{}, false
	}
	return service.SenderSpec{ID: id, Kind: kind, Workspace: workspaceFromRequest(r)}, true
}

func parsePage(r *http.Request) message.Page {
	offset := parseIntDefault(r.URL.Query().Get("offset"), 0)
	limit := parseIntDefault(r.URL.Query().Get("limit"), 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return message.Page{Offset: offset, Limit: limit}
}

func parseIntDefault(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func workspaceFromRequest(r *http.Request) string {
	if workspace := strings.TrimSpace(r.Header.Get("X-AGENT-WORKSPACE")); workspace != "" {
		return workspace
	}
	return strings.TrimSpace(r.URL.Query().Get("workspace"))
}

func idsFromRequest(r *http.Request) ([]string, error) {
	idsJSON := strings.TrimSpace(r.URL.Query().Get("ids_json"))
	if idsJSON == "" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(idsJSON), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, service.ErrorResponse{Error: msg})
}
