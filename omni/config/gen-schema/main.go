package main

import (
	"encoding/json"
	"os"

	"github.com/Shaik-Sirajuddin/omni/config"
	"github.com/invopop/jsonschema"
)

func main() {
	r := &jsonschema.Reflector{
		AllowAdditionalProperties: false,
	}
	schema := r.Reflect(&config.OmniConfig{})
	schema.ID = jsonschema.ID("https://omni.sh/config/" + config.SchemaVersion + "/config_schema.json")

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(schema); err != nil {
		panic(err)
	}
}
