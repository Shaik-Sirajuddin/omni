SCHEMA_FILE  := ./spec/provision_schema.json
OPENAPI_FILE := ./spec/openapi.yaml

.PHONY: gen-provision-schema
gen-provision-schema:
	mkdir -p spec
	go run ./gen-schema > $(SCHEMA_FILE)

.PHONY: gen-provision-openapi
gen-provision-openapi:
	@echo "openapi.yaml is hand-authored at $(OPENAPI_FILE)"

.PHONY: gen
gen: gen-provision-schema gen-provision-openapi

.PHONY: clean
clean:
	rm -f $(SCHEMA_FILE)
