COMPOSE_FILE  := dev/docker-compose.yaml
VERSION       ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo "dev")
WORKTREE_NAME := $(notdir $(CURDIR))
IMAGE_TAG     := omni-dev:$(WORKTREE_NAME)
export COMPOSE_PROJECT_NAME := omni-$(WORKTREE_NAME)

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
	@bash dev/build.sh

install:
	@sudo bash dev/install.sh

uninstall:
	@sudo systemctl disable --now omni@$(shell id -un) 2>/dev/null || true
	@sudo rm -f /etc/systemd/system/omni@.service
	@sudo rm -f /usr/local/bin/omni /usr/local/bin/omni-server
	@sudo rm -rf /opt/omni
	@sudo systemctl daemon-reload
	@echo "==> uninstalled"

# ── dev preflight ─────────────────────────────────────────────────────────────

dev-preflight:
	@if [ -e dev/local ] && [ "$$(stat -c '%U' dev/local 2>/dev/null || echo root)" != "$$(id -un)" ]; then \
	    echo "==> dev/local is root-owned (docker ran before preflight); fixing ownership..."; \
	    sudo chown -R "$$(id -un)" dev/local; \
	fi
	@mkdir -p dev/local/agents dev/local/omni
	@if [ ! -e dev/local/shared ]; then \
	    main=$$(git worktree list --porcelain | head -1 | awk '{print $$2}'); \
	    if [ -d "$$main/dev/local/shared" ]; then \
	        ln -s "$$main/dev/local/shared" dev/local/shared \
	            || { echo "ERROR: cannot create symlink dev/local/shared -> $$main/dev/local/shared"; \
	                 echo "       dev/local/ may be root-owned; run: sudo chown -R $$(id -un) dev/local/"; \
	                 exit 1; }; \
	        echo "linked dev/local/shared -> $$main/dev/local/shared"; \
	    else \
	        mkdir -p dev/local/shared \
	            || { echo "ERROR: cannot mkdir dev/local/shared"; exit 1; }; \
	        echo "created dev/local/shared/ (no main worktree shared found)"; \
	    fi \
	fi
	@mkdir -p dev/local/shared/.codex dev/local/shared/.gemini/antigravity-cli 2>/dev/null || true
	@[ -f dev/local/shared/.codex/auth.json ]                                || echo '{}' > dev/local/shared/.codex/auth.json
	@[ -f dev/local/shared/.gemini/antigravity-cli/antigravity-oauth-token ] || touch dev/local/shared/.gemini/antigravity-cli/antigravity-oauth-token
	@[ -f dev/local/shared/.env.docker ] || { cp dev/local.example/.env.docker.example dev/local/shared/.env.docker && echo "created dev/local/shared/.env.docker"; }
	@echo "==> preflight done — edit dev/local/shared/.env.docker before docker-up"

# ── docker ────────────────────────────────────────────────────────────────────

docker-build:
	OMNI_DEV_IMAGE=$(IMAGE_TAG) docker compose -f $(COMPOSE_FILE) build --build-arg VERSION=$(VERSION)

docker-fix-volumes:
	@vol=$$(docker volume ls --format '{{.Name}}' | grep '_agent-codex$$' | head -1); \
	 [ -z "$$vol" ] || docker run --rm -v "$$vol":/data alpine sh -c \
	    '[ -d /data/auth.json ] && rm -rf /data/auth.json && echo "fixed $$vol: auth.json was a directory" || true' 2>/dev/null || true
	@vol=$$(docker volume ls --format '{{.Name}}' | grep '_agent-gemini$$' | head -1); \
	 [ -z "$$vol" ] || docker run --rm -v "$$vol":/data alpine sh -c \
	    '[ -d /data/antigravity-cli/antigravity-oauth-token ] && rm -rf /data/antigravity-cli/antigravity-oauth-token && echo "fixed $$vol: antigravity-oauth-token was a directory" || true' 2>/dev/null || true

docker-up: dev-preflight docker-fix-volumes
	OMNI_DEV_IMAGE=$(IMAGE_TAG) docker compose -f $(COMPOSE_FILE) up -d --wait
	docker compose -f $(COMPOSE_FILE) exec ubuntu bash -l

docker-down:
	docker compose -f $(COMPOSE_FILE) down

# rebuild image and restart container in one step
docker-rebuild: docker-build docker-down docker-up

# restart container without rebuilding image
docker-relaunch: docker-down docker-up

docker-connect:
	docker compose -f $(COMPOSE_FILE) exec ubuntu bash -l
