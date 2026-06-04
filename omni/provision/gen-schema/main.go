package main

import (
	"encoding/json"
	"os"

	"github.com/Shaik-Sirajuddin/memory/provision"
	"github.com/invopop/jsonschema"
)

func main() {
	r := &jsonschema.Reflector{
		AllowAdditionalProperties: false,
	}
	schema := r.Reflect(&provision.ProvisionLayout{})
	schema.ID = jsonschema.ID("https://omni.sh/provision/v1/provision_schema.json")

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(schema); err != nil {
		panic(err)
	}
}
