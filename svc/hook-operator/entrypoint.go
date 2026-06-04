package hookoperator

import (
	"context"
	"strings"

)

type entryPoint struct {
	cache    *entryCache
	exec     *executor
	enricher *enricher
}

func newEntryPoint(cache *entryCache, exec *executor, enricher *enricher) *entryPoint {
	return &entryPoint{cache: cache, exec: exec, enricher: enricher}
}

// Hook enriches the payload with omni context, reads hook entries from cache,
// executes them all in parallel, and returns the aggregated Result.
func (ep *entryPoint) Hook(payload HookPayload) (Result, error) {
	entries := ep.cache.get(payload.EventName)
	logger.Debug("hook: received", "event", payload.EventName, "entries", len(entries))

	if len(entries) == 0 {
		logger.Debug("hook: no entries configured, passing through", "event", payload.EventName)
		return Result{Continue: true}, nil
	}

	payload.Body = ep.enricher.enrich(payload.Body)

	results := ep.exec.runAll(context.Background(), payload, entries)
	result := aggregate(results)

	logger.Debug("hook: aggregated result", "event", payload.EventName, "continue", result.Continue, "suppress_output", result.SuppressOutput)

	if result.Continue {
		logger.Info("hook: continue", "event", payload.EventName)
	} else {
		reason := ""
		if result.StopReason != nil {
			reason = *result.StopReason
		}
		logger.Info("hook: blocked", "event", payload.EventName, "stop_reason", reason)
	}

	if result.SystemMessage != nil && *result.SystemMessage != "" {
		logger.Info("hook: system_message", "event", payload.EventName, "message", *result.SystemMessage)
	}

	return result, nil
}

// aggregate merges individual hook results into a single Result.
//
// Rules:
//   - Any continue=false blocks; the first stop_reason encountered wins.
//   - All system_messages are joined with a newline.
//   - SuppressOutput is true when any hook sets it to true.
//   - Errored hooks are safe-skipped; delivery continues with remaining results.
func aggregate(results []hookRunResult) Result {
	out := Result{Continue: true}

	var sysMessages []string
	var skipped int

	for _, r := range results {
		if r.err != nil {
			skipped++
			continue
		}

		if !r.resp.Continue && out.Continue {
			out.Continue = false
			out.StopReason = r.resp.StopReason
		}

		if r.resp.SuppressOutput {
			out.SuppressOutput = true
		}

		if r.resp.SystemMessage != nil && *r.resp.SystemMessage != "" {
			sysMessages = append(sysMessages, *r.resp.SystemMessage)
		}
	}

	if len(sysMessages) > 0 {
		merged := strings.Join(sysMessages, "\n")
		out.SystemMessage = &merged
	}

	if skipped > 0 {
		logger.Warn("hook: some entries failed, delivery continued with partial results", "skipped", skipped, "total", len(results))
	}

	return out
}
