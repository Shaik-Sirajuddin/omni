package broadcast

import (
	"context"
	"fmt"
)

type dispatcherSet struct {
	http Dispatcher
	unix Dispatcher
	cli  Dispatcher
}

func (d dispatcherSet) Dispatch(ctx context.Context, entry MCPClientEntry, payload CallbackPayload) error {
	logger.Debug("broadcast dispatcher selecting",
		"server_id", entry.ServerID,
		"message_id", payload.MessageID,
		"callback_type", entry.CallbackType,
	)
	switch entry.CallbackType {
	case CallbackHTTP:
		return d.dispatch(ctx, d.http, entry, payload)
	case CallbackHTTPOverUnix:
		return d.dispatch(ctx, d.unix, entry, payload)
	case CallbackAGCLI:
		return d.dispatch(ctx, d.cli, entry, payload)
	default:
		return fmt.Errorf("%w: %s", ErrUnknownCallback, entry.CallbackType)
	}
}

func (d dispatcherSet) dispatch(ctx context.Context, dispatcher Dispatcher, entry MCPClientEntry, payload CallbackPayload) error {
	if dispatcher == nil {
		err := fmt.Errorf("dispatcher not configured for callback_type %s", entry.CallbackType)
		logger.Error("broadcast dispatcher missing", "err", err, "callback_type", entry.CallbackType, "server_id", entry.ServerID)
		return err
	}
	if err := dispatcher.Dispatch(ctx, entry, payload); err != nil {
		logger.Error("broadcast dispatcher failed", "err", err, "callback_type", entry.CallbackType, "server_id", entry.ServerID, "message_id", payload.MessageID)
		return err
	}
	logger.Debug("broadcast dispatcher completed", "callback_type", entry.CallbackType, "server_id", entry.ServerID, "message_id", payload.MessageID)
	return nil
}
