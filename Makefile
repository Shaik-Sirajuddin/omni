COMPOSE_FILE := development/docker-compose.yaml
VERSION      ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo "dev")

.PHONY: build install uninstall release snapshot docker-build docker-up docker-down docker-rebuild docker-relaunch docker-connect dev-preflight docker-fix-volumes tools

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
	@mkdir -p development/local/agents
	@if [ ! -e development/local/shared ]; then \
	    main=$$(git worktree list --porcelain | head -1 | awk '{print $$2}'); \
	    if [ -d "$$main/development/local/shared" ]; then \
	        ln -s "$$main/development/local/shared" development/local/shared && echo "linked development/local/shared -> $$main/development/local/shared"; \
	    else \
	        mkdir -p development/local/shared && echo "created development/local/shared/ (no main worktree shared found)"; \
	    fi \
	fi
	@mkdir -p development/local/shared/.codex development/local/shared/.gemini/antigravity-cli 2>/dev/null || true
	@[ -f development/local/shared/.codex/auth.json ]                                || echo '{}' > development/local/shared/.codex/auth.json
	@[ -f development/local/shared/.gemini/antigravity-cli/antigravity-oauth-token ] || touch development/local/shared/.gemini/antigravity-cli/antigravity-oauth-token
	@[ -f development/local/shared/.env.docker ] || { cp development/local.example/.env.docker.example development/local/shared/.env.docker && echo "created development/local/shared/.env.docker"; }
	@echo "==> preflight done — edit development/local/shared/.env.docker before docker-up"

# ── docker ────────────────────────────────────────────────────────────────────

docker-build:
	docker compose -f $(COMPOSE_FILE) build --build-arg VERSION=$(VERSION)

docker-fix-volumes:
	@vol=$$(docker volume ls --format '{{.Name}}' | grep '_agent-codex$$' | head -1); \
	 [ -z "$$vol" ] || docker run --rm -v "$$vol":/data alpine sh -c \
	    '[ -d /data/auth.json ] && rm -rf /data/auth.json && echo "fixed $$vol: auth.json was a directory" || true' 2>/dev/null || true
	@vol=$$(docker volume ls --format '{{.Name}}' | grep '_agent-gemini$$' | head -1); \
	 [ -z "$$vol" ] || docker run --rm -v "$$vol":/data alpine sh -c \
	    '[ -d /data/antigravity-cli/antigravity-oauth-token ] && rm -rf /data/antigravity-cli/antigravity-oauth-token && echo "fixed $$vol: antigravity-oauth-token was a directory" || true' 2>/dev/null || true

docker-up: dev-preflight docker-fix-volumes
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
