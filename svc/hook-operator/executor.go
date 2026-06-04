package hookoperator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Shaik-Sirajuddin/memory/config"

)

const defaultHookTimeout = 10 * time.Second

// hookResponse is the JSON shape every hook command / HTTP endpoint must return.
type hookResponse struct {
	Continue       bool    `json:"continue"`
	SuppressOutput bool    `json:"suppress_output"`
	StopReason     *string `json:"stop_reason,omitempty"`
	SystemMessage  *string `json:"system_message,omitempty"`
}

type hookRunResult struct {
	resp hookResponse
	err  error
}

type executor struct{}

func newExecutor() *executor { return &executor{} }

// runAll executes all entries in parallel and returns their individual results.
func (e *executor) runAll(ctx context.Context, payload HookPayload, entries []config.HookEntry) []hookRunResult {
	results := make([]hookRunResult, len(entries))
	var wg sync.WaitGroup
	for i, entry := range entries {
		wg.Add(1)
		go func(idx int, ent config.HookEntry) {
			defer wg.Done()
			results[idx] = e.run(ctx, payload, ent)
		}(i, entry)
	}
	wg.Wait()
	return results
}

func (e *executor) run(ctx context.Context, payload HookPayload, entry config.HookEntry) hookRunResult {
	timeout := defaultHookTimeout
	if entry.Timeout != nil && *entry.Timeout > 0 {
		timeout = time.Duration(*entry.Timeout * float64(time.Second))
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if entry.Url != nil && *entry.Url != "" {
		return e.runHTTP(ctx, payload, *entry.Url)
	}
	if entry.Command != nil && *entry.Command != "" {
		return e.runCommand(ctx, payload, *entry.Command, entry.Args)
	}
	return hookRunResult{err: fmt.Errorf("hook-operator: entry has neither command nor url")}
}

// runCommand execs the binary, writes payload.Body to stdin, reads JSON from stdout.
func (e *executor) runCommand(ctx context.Context, payload HookPayload, command string, args []string) hookRunResult {
	logger.Debug("hook: exec command", "event", payload.EventName, "command", command, "args", args)

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdin = bytes.NewReader(payload.Body)

	out, err := cmd.Output()
	if err != nil {
		logger.Error("hook: command failed", "err", err, "event", payload.EventName, "command", command)
		return hookRunResult{err: fmt.Errorf("hook-operator: command %q: %w", command, err)}
	}

	var resp hookResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		logger.Error("hook: parse command response failed", "err", err, "command", command)
		return hookRunResult{err: fmt.Errorf("hook-operator: parse command response: %w", err)}
	}

	logger.Debug("hook: command response", "event", payload.EventName, "command", command, "continue", resp.Continue, "suppress_output", resp.SuppressOutput)
	return hookRunResult{resp: resp}
}

// runHTTP sends the payload to an HTTP or HTTP-over-unix-socket endpoint.
//
// URL schemes supported:
//
//	http://host/path           — plain TCP HTTP
//	https://host/path          — TLS HTTP
//	unix:///path/to.sock/route — HTTP over unix socket; route is the HTTP path
func (e *executor) runHTTP(ctx context.Context, payload HookPayload, rawURL string) hookRunResult {
	client, httpURL, err := resolveHTTPClient(rawURL)
	if err != nil {
		return hookRunResult{err: err}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL, bytes.NewReader(payload.Body))
	if err != nil {
		logger.Error("hook: build http request failed", "err", err, "url", rawURL)
		return hookRunResult{err: fmt.Errorf("hook-operator: build request for %q: %w", rawURL, err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hook-Event", payload.EventName)

	resp, err := client.Do(req)
	if err != nil {
		logger.Warn("hook: http request failed (safe-skipped)", "err", err, "event", payload.EventName, "url", rawURL)
		return hookRunResult{err: fmt.Errorf("hook-operator: http %q: %w", rawURL, err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error("hook: read http response failed", "err", err, "url", rawURL)
		return hookRunResult{err: fmt.Errorf("hook-operator: read response from %q: %w", rawURL, err)}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Warn("hook: http non-2xx response (safe-skipped)", "status", resp.StatusCode, "url", rawURL, "event", payload.EventName)
		return hookRunResult{err: fmt.Errorf("hook-operator: http %q returned %d", rawURL, resp.StatusCode)}
	}

	var hookResp hookResponse
	if err := json.Unmarshal(body, &hookResp); err != nil {
		logger.Error("hook: parse http response failed", "err", err, "url", rawURL, "status", resp.StatusCode)
		return hookRunResult{err: fmt.Errorf("hook-operator: parse http response from %q: %w", rawURL, err)}
	}

	logger.Debug("hook: http response", "event", payload.EventName, "url", rawURL, "status", resp.StatusCode, "continue", hookResp.Continue)
	return hookRunResult{resp: hookResp}
}

// resolveHTTPClient returns the appropriate http.Client and normalised URL.
// For unix:// URLs the client dials via the unix socket; the returned URL
// uses http://localhost so net/http can form a valid request.
func resolveHTTPClient(rawURL string) (*http.Client, string, error) {
	const unixScheme = "unix://"
	if !strings.HasPrefix(rawURL, unixScheme) {
		return &http.Client{}, rawURL, nil
	}

	// unix:///tmp/hook.sock/route  →  socketPath=/tmp/hook.sock  httpPath=/route
	rest := strings.TrimPrefix(rawURL, unixScheme)
	// rest may look like /tmp/hook.sock/route or /tmp/hook.sock
	// Split on the first occurrence of a path segment after the socket file.
	socketPath, httpPath := splitUnixURL(rest)

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport}
	return client, "http://localhost" + httpPath, nil
}

// splitUnixURL splits a unix socket path from its HTTP route.
// Input: /tmp/hook.sock/api/v1  →  /tmp/hook.sock , /api/v1
// Input: /tmp/hook.sock         →  /tmp/hook.sock , /
func splitUnixURL(path string) (socketPath, httpPath string) {
	// Walk forward finding the first component after the socket file extension.
	// Heuristic: socket path ends at ".sock" or at the segment before the
	// first path segment that does not exist as part of the socket filename.
	const sockSuffix = ".sock"
	idx := strings.Index(path, sockSuffix)
	if idx != -1 {
		end := idx + len(sockSuffix)
		socketPath = path[:end]
		httpPath = path[end:]
		if httpPath == "" {
			httpPath = "/"
		}
		return socketPath, httpPath
	}
	// Fallback: treat the whole thing as the socket path.
	return path, "/"
}
