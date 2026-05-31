package main

import (
	"encoding/json"
	"os"

	confhooks "github.com/Shaik-Sirajuddin/memory/config/hooks"
	"github.com/invopop/jsonschema"
)

// hookPayloads aggregates all hook wire types so the reflector
// produces $defs for every input and the shared response in one schema.
type hookPayloads struct {
	PreToolUse         confhooks.PreToolUseInput
	PostToolUse        confhooks.PostToolUseInput
	PostToolUseFailure confhooks.PostToolUseFailureInput
	SessionStart    confhooks.SessionStartInput
	SessionEnd   confhooks.SessionEndInput
	PrePrompt          confhooks.PrePromptInput
	PostPrompt         confhooks.PostPromptInput
	Response           confhooks.Response
}

func main() {
	r := &jsonschema.Reflector{
		AllowAdditionalProperties: false,
	}
	schema := r.Reflect(&hookPayloads{})
	schema.ID = jsonschema.ID("https://omni.sh/config/hooks/" + confhooks.SchemaVersion + "/hooks_schema.json")

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(schema); err != nil {
		panic(err)
	}
}
