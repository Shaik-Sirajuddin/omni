package broadcast

import (
	"context"
	"time"

	omnicli "github.com/Shaik-Sirajuddin/memory/mcp/engine/impl/cli"
	"gopkg.in/yaml.v3"
)

type SessionExecutor interface {
	ExecInSession(ctx context.Context, agentID, agentName, workspace, prompt string) error
}

type CLIDispatcher struct {
	binary   string
	executor SessionExecutor
	timeout  time.Duration
}

func NewCLIDispatcher(binary string) *CLIDispatcher {
	logger.Debug("broadcast cli dispatcher initializing", "binary", binary)
	return &CLIDispatcher{
		binary:   binary,
		executor: omnicli.New(binary),
		timeout:  10 * time.Second,
	}
}

func NewCLIDispatcherWithExecutor(binary string, executor SessionExecutor) *CLIDispatcher {
	d := NewCLIDispatcher(binary)
	d.executor = executor
	logger.Debug("broadcast cli dispatcher executor overridden", "binary", binary)
	return d
}

func (d *CLIDispatcher) Dispatch(ctx context.Context, entry MCPClientEntry, payload CallbackPayload) error {
	logger.Debug("broadcast cli dispatch preparing", "agent_id", entry.ServerID, "message_id", payload.MessageID, "workspace", entry.Endpoint, "binary", d.binary)
	prompt, err := buildSuccessPrompt(entry, payload)
	if err != nil {
		logger.Error("broadcast cli dispatch prompt build failed", "err", err, "agent_id", entry.ServerID, "message_id", payload.MessageID)
		return err
	}
	logger.Debug("broadcast cli dispatch prompt built", "agent_id", entry.ServerID, "message_id", payload.MessageID, "bytes", len(prompt))
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	if err := d.executor.ExecInSession(ctx, entry.ServerID, entry.AgentID, entry.Endpoint, prompt); err != nil {
		logger.Error("broadcast cli dispatch failed", "err", err, "agent_id", entry.ServerID, "message_id", payload.MessageID, "binary", d.binary)
		return err
	}
	logger.Debug("broadcast cli dispatch completed", "agent_id", entry.ServerID, "message_id", payload.MessageID, "binary", d.binary)
	return nil
}

type successPromptItem struct {
	MessageID   string `yaml:"message_id"`
	RespondedTo string `yaml:"responded_to,omitempty"`
	Refs        string `yaml:"refs"`
	Prompt      string `yaml:"prompt"`
	Status      string `yaml:"status"`
	DeliveryAt  *int64 `yaml:"delivery_time,omitempty"`
}

type successPromptPayload struct {
	RequestType string              `yaml:"request_type"`
	Instruction string              `yaml:"instruction"`
	Messages    []successPromptItem `yaml:"messages"`
}

func buildSuccessPrompt(entry MCPClientEntry, payload CallbackPayload) (string, error) {
	out, err := yaml.Marshal(successPromptPayload{
		RequestType: "callback_success",
		Instruction: "The following message delivery succeeded. Record the success and continue with any pending work.",
		Messages: []successPromptItem{{
			MessageID:   payload.MessageID,
			RespondedTo: payload.RespondedTo,
			Refs:        string(payload.Refs),
			Prompt:      payload.Prompt,
			Status:      payload.Status,
			DeliveryAt:  payload.DeliveryTime,
		}},
	})
	if err != nil {
		logger.Error("broadcast success prompt yaml marshal failed", "err", err, "agent_id", entry.ServerID, "message_id", payload.MessageID)
		return "", err
	}
	return string(out), nil
}
