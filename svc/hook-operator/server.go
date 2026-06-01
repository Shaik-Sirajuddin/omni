package hookoperator

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/Shaik-Sirajuddin/omni/connector/codeagent"
)

type hookServer struct {
	ep        HookEntryPoint
	ar        *agentRegistry
	mu        sync.Mutex
	servers   []*http.Server
	listeners []net.Listener
}

func newServer(ep HookEntryPoint, ar *agentRegistry) *hookServer {
	return &hookServer{ep: ep, ar: ar}
}

// start begins serving on the given network/address pairs.
func (s *hookServer) start(ctx context.Context, listeners []net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/hook-callback", s.handleHookCallback)
	mux.HandleFunc("/agents/status", s.handleAgentsStatus)
	mux.HandleFunc("/providers/", s.handleProviders)

	for _, ln := range listeners {
		srv := &http.Server{Handler: mux}

		s.mu.Lock()
		s.servers = append(s.servers, srv)
		s.listeners = append(s.listeners, ln)
		s.mu.Unlock()

		go func(srv *http.Server, ln net.Listener) {
			_ = srv.Serve(ln)
		}(srv, ln)

		go func(srv *http.Server) {
			<-ctx.Done()
			_ = srv.Close()
		}(srv)
	}
	return nil
}

func (s *hookServer) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, srv := range s.servers {
		_ = srv.Close()
	}
	s.servers = nil
	s.listeners = nil
}

func (s *hookServer) handleHookCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload HookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}

	result, err := s.ep.Hook(payload)
	if err != nil {
		http.Error(w, fmt.Sprintf("hook error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// GET /agents/status — returns verification status for all registered providers.
func (s *hookServer) handleAgentsStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	statuses := s.ar.Status()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statuses)
}

// POST /providers/{name}/apply — registers and applies default hooks for a provider.
// The request body must be empty; the transformer must already be registered via Register().
// This endpoint re-applies hooks (idempotent).
func (s *hookServer) handleProviders(w http.ResponseWriter, r *http.Request) {
	// Expect: /providers/{name}/apply
	path := strings.TrimPrefix(r.URL.Path, "/providers/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[1] != "apply" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	provider := codeagent.Provider(parts[0])
	ok, missing, err := s.ar.Verify(provider)
	if err != nil {
		http.Error(w, fmt.Sprintf("verify failed: %v", err), http.StatusInternalServerError)
		return
	}
	if !ok && len(missing) > 0 {
		// Provider is owned but entries are missing — re-apply.
		transformer, exists := s.ar.transformers[provider]
		if !exists {
			http.Error(w, fmt.Sprintf("provider %q not found", provider), http.StatusNotFound)
			return
		}
		if err := s.ar.reg.apply(transformer); err != nil {
			http.Error(w, fmt.Sprintf("apply failed: %v", err), http.StatusInternalServerError)
			return
		}
		ok, missing, _ = s.ar.Verify(provider)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ProviderHookStatus{
		Provider: provider,
		OK:       ok,
		Missing:  missing,
	})
}
