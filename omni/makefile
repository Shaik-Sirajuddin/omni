# =========================
# Targets
# =========================

.PHONY: release
release:
	cd .. && goreleaser release --clean

.PHONY: snapshot
snapshot:
	cd .. && goreleaser release --snapshot --clean

.PHONY: release-check
release-check:
	cd .. && goreleaser check

.PHONY: build
build:
	bash ../development/build.sh

.PHONY: install
install:
	sudo DEBUG=$(DEBUG) bash ../development/install.sh

.PHONY: test-unit
test-unit:
	go test -tags=unit ./...

.PHONY: test-integration
test-integration:
	go test -tags=integration ./...

.PHONY: test-codex-integration
test-codex-integration:
	go test -tags=integration ./connector/codeagent/codex/...

.PHONY: setup-test-infra
setup-test-infra:
	set -a; . ../svc/hook-operator/testclient/.env; set +a; cd ../svc/hook-operator && go run ./testclient/cmd

.PHONY: generate-schema
generate-schema:
	$(MAKE) -C connector/codeagent/claude gen-structs-from-schema
	$(MAKE) -C connector/codeagent/codex gen-structs-from-schema
	$(MAKE) -C connector/codeagent/gemini gen-structs-from-schema

.PHONY: generate-schema-from-structs
generate-schema-from-structs:
	$(MAKE) -C connector/codeagent/claude gen-schema-from-structs
	$(MAKE) -C connector/codeagent/gemini gen-schema-from-structs

.PHONY: export-config-schema
export-config-schema:
	$(MAKE) -C config export-config-schema

# Clean generated file
.PHONY: clean
clean:
	rm -f $(OUTPUT_FILE)
