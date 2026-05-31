package hooks

const (
	SessionStart HookID = "SessionStart"
	SessionEnd   HookID = "SessionEnd"
)

type SessionStartParams struct {
	HookInput
	Source string `json:"source"` // startup | resume | clear | compact
}

type SessionEndParams struct {
	HookInput
}

type SessionStartResult struct {
	HookOuput
}

type SessionEndResult struct {
	HookOuput
}
