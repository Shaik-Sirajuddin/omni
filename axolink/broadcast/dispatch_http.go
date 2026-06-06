package broadcast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type HTTPDispatcher struct {
	client *http.Client
}

func NewHTTPDispatcher(timeout time.Duration) *HTTPDispatcher {
	logger.Debug("broadcast http dispatcher initializing", "timeout", timeout)
	return &HTTPDispatcher{
		client: &http.Client{Timeout: timeout},
	}
}

func (d *HTTPDispatcher) Dispatch(ctx context.Context, entry MCPClientEntry, payload CallbackPayload) error {
	logger.Debug("broadcast http dispatch preparing",
		"server_id", entry.ServerID,
		"message_id", payload.MessageID,
		"endpoint", entry.Endpoint,
		"auth_ref_present", entry.AuthenticationRef != "",
	)
	body, err := json.Marshal(payload)
	if err != nil {
		logger.Error("broadcast http dispatch marshal failed", "err", err, "server_id", entry.ServerID, "message_id", payload.MessageID)
		return err
	}
	logger.Debug("broadcast http dispatch payload encoded", "server_id", entry.ServerID, "message_id", payload.MessageID, "bytes", len(body))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, entry.Endpoint, bytes.NewReader(body))
	if err != nil {
		logger.Error("broadcast http dispatch request build failed", "err", err, "server_id", entry.ServerID, "endpoint", entry.Endpoint)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if entry.AuthenticationRef != "" {
		req.Header.Set("Authorization", "Bearer "+entry.AuthenticationRef)
	}

	logger.Debug("broadcast http dispatch sending", "server_id", entry.ServerID, "message_id", payload.MessageID, "endpoint", entry.Endpoint)
	resp, err := d.client.Do(req)
	if err != nil {
		logger.Error("broadcast http dispatch request failed", "err", err, "server_id", entry.ServerID, "message_id", payload.MessageID, "endpoint", entry.Endpoint)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		err := fmt.Errorf("http callback failed: status=%d body=%q", resp.StatusCode, string(data))
		logger.Error("broadcast http dispatch response failed", "err", err, "server_id", entry.ServerID, "message_id", payload.MessageID, "status", resp.StatusCode)
		return err
	}
	logger.Debug("broadcast http dispatch response received", "server_id", entry.ServerID, "message_id", payload.MessageID, "status", resp.StatusCode)
	return nil
}
