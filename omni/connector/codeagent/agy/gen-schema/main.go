package main

import (
	"encoding/json"
	"os"

	agy "github.com/Shaik-Sirajuddin/memory/connector/codeagent/agy"
	"github.com/google/jsonschema-go/jsonschema"
)

func main() {
	schema, err := jsonschema.For[agy.SettingsSchemaJson](nil)
	if err != nil {
		panic(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(schema); err != nil {
		panic(err)
	}
}
