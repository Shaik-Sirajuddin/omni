package main

import (
	"encoding/json"
	"os"

	claude "github.com/Shaik-Sirajuddin/omni/connector/codeagent/claude"
	"github.com/google/jsonschema-go/jsonschema"
)

func main() {
	schema, err := jsonschema.For[claude.SettingsSchemaJson](nil)
	if err != nil {
		panic(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(schema); err != nil {
		panic(err)
	}
}
