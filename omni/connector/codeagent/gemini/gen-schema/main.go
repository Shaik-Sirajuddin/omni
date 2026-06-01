package main

import (
	"encoding/json"
	"os"

	gemini "github.com/Shaik-Sirajuddin/omni/connector/codeagent/gemini"
	"github.com/google/jsonschema-go/jsonschema"
)

func main() {
	schema, err := jsonschema.For[gemini.SettingsSchemaJson](nil)
	if err != nil {
		panic(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(schema); err != nil {
		panic(err)
	}
}
