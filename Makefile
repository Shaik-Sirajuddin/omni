COMPOSE_FILE := development/docker-compose.yaml
VERSION      ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo "dev")

.PHONY: build install uninstall release snapshot gen docker-build docker-up docker-down docker-rebuild docker-relaunch docker-connect dev-preflight tools

# ── codegen ───────────────────────────────────────────────────────────────────

gen:
	$(MAKE) -C omni gen

# ── tools ────────────────────────────────────────────────────────────────────

tools:
	go install github.com/goreleaser/goreleaser/v2@latest

# ── release ───────────────────────────────────────────────────────────────────

release:
	GITHUB_TOKEN=$${GITHUB_TOKEN:-$(shell gh auth token)} goreleaser release --clean

snapshot:
	GITHUB_TOKEN=$${GITHUB_TOKEN:-$(shell gh auth token)} goreleaser release --snapshot --clean

# ── local (build-from-source) ─────────────────────────────────────────────────

build:
	@bash development/build.sh

install:
	@sudo bash development/install.sh

uninstall:
	@sudo systemctl disable --now omni@$(shell id -un) 2>/dev/null || true
	@sudo rm -f /etc/systemd/system/omni@.service
	@sudo rm -f /usr/local/bin/omni /usr/local/bin/omni-server
	@sudo rm -rf /opt/omni
	@sudo systemctl daemon-reload
	@echo "==> uninstalled"

# ── dev preflight ─────────────────────────────────────────────────────────────

dev-preflight:
	@[ -f development/.env.docker ] || { cp development/.env.docker.example development/.env.docker && echo "created development/.env.docker"; }
	@[ -d development/local ] || { cp -r development/local.example development/local && echo "created development/local/"; }
	@echo "==> preflight done — edit development/.env.docker and development/local/ before docker-up"

# ── docker ────────────────────────────────────────────────────────────────────

docker-build:
	docker compose -f $(COMPOSE_FILE) build --build-arg VERSION=$(VERSION)

docker-up:
	docker compose -f $(COMPOSE_FILE) up -d --wait
	docker compose -f $(COMPOSE_FILE) exec ubuntu bash -l

docker-down:
	docker compose -f $(COMPOSE_FILE) down

# rebuild image and restart container in one step
docker-rebuild: docker-build docker-down docker-up

# restart container without rebuilding image
docker-relaunch: docker-down docker-up

docker-connect:
	docker compose -f $(COMPOSE_FILE) exec ubuntu bash -l
