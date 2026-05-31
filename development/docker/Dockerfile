FROM ubuntu:22.04

# systemd inside container
ENV container=docker
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    systemd systemd-sysv \
    curl wget git sudo \
    ca-certificates gnupg \
    make build-essential \
    nano \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# disable unnecessary systemd units
RUN find /etc/systemd/system \
         /lib/systemd/system \
    -path '*.wants/*' \
    \( -name '*getty*' -o -name '*plymouth*' -o -name '*udev*' \
       -o -name '*apt-daily*' \) \
    -exec rm -f {} + 2>/dev/null || true

# Go toolchain
ARG GO_VERSION=1.23.0
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
    | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:$PATH"

# Agent CLIs — pinned versions, baked into image (update ARGs to bump)
ARG CLAUDE_CODE_VERSION=2.1.145
ARG CODEX_VERSION=0.132.0
ARG GEMINI_CLI_VERSION=0.42.0
ARG NODE_VERSION=20.18.0

# Claude Code — official binary installer; npm path deprecated by Anthropic
RUN curl -fsSL https://claude.ai/install.sh -o /tmp/install-claude.sh \
    && CLAUDE_CODE_VERSION=${CLAUDE_CODE_VERSION} bash /tmp/install-claude.sh \
    && rm /tmp/install-claude.sh

# Codex — Rust binary from GitHub releases; no Node required
RUN curl -fL "https://github.com/openai/codex/releases/download/v${CODEX_VERSION}/codex-x86_64-unknown-linux-musl.tar.gz" \
    | tar -xz -C /usr/local/bin \
    && chmod +x /usr/local/bin/codex

# Node.js — minimal official tarball, needed only for Gemini CLI (no nodesource, no pnpm)
RUN curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" \
    | tar -xJ --strip-components=1 -C /usr/local

# Gemini CLI — npm-only distribution; no standalone binary available upstream
RUN npm install -g @google/gemini-cli@${GEMINI_CLI_VERSION} \
    && npm cache clean --force

# Agy (Antigravity CLI by Google) — installs to ~/.local/bin/agy
RUN curl -fsSL https://antigravity.google/cli/install.sh | bash \
    && ln -sf /root/.local/bin/agy /usr/local/bin/agy

# ── build omni binaries into image ────────────────────────────────────────────
WORKDIR /build

COPY omni/            omni/
COPY svc/             svc/
COPY pkg/             pkg/
COPY tunnel_mcp/      tunnel_mcp/
COPY development/     development/
COPY deployment/      deployment/

ARG VERSION=dev
ENV VERSION=${VERSION}
ENV OMNI_PREFIX=/opt/omni
# Module cache is mounted across builds — no re-download unless go.mod/go.sum change
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    OUT_DIR=/opt/omni/bin bash development/build.sh

# ── runtime image setup ───────────────────────────────────────────────────────
ENV WORKSPACE=/build
WORKDIR /build

COPY development/docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

VOLUME ["/var/lib/omni-root", "/root/.config"]

ENTRYPOINT ["/entrypoint.sh"]
