package hooks

type InputParser interface {
	PreToolUseParams(any) (*PreToolUseParams, error)
	PostToolUseParams(any) (*PostToolUseParams, error)
	PostToolUseFailureParams(any) (*PostToolUseFailureParams, error)
	SessionStartParams(any) (*SessionStartParams, error)
	SessionEndParams(any) (*SessionEndParams, error)
	PrePromptInputParams(any) (*PrePromptInputParams, error)
	PostPromptInputParams(any) (*PostPromptInputParams, error)
}

type OutputParser interface {
	PreToolUseResult(any) (*PreToolUseResult, error)
	PostToolUseResult(any) (*PostToolUseResult, error)
	PostToolUseFailureResult(any) (*PostToolUseFailureResult, error)
	SessionStartResult(any) (*SessionStartResult, error)
	SessionEndResult(any) (*SessionEndResult, error)
	PrePromptInputResult(any) (*PrePromptInputResult, error)
	PostPromptInputResult(any) (*PostPromptInputResult, error)
}

type HookIOParser interface {
	InputParser
	OutputParser
}
