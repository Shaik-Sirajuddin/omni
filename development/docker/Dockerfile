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

# Node + pnpm
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y nodejs \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g pnpm

# Agent CLIs — pinned versions, baked into image (update ARGs to bump)
ARG CLAUDE_CODE_VERSION=2.1.145
ARG CODEX_VERSION=0.132.0
ARG GEMINI_CLI_VERSION=0.42.0
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
    && npm install -g @openai/codex@${CODEX_VERSION} \
    && npm install -g @google/gemini-cli@${GEMINI_CLI_VERSION} \
    && ln -sf /usr/lib/node_modules/@anthropic-ai/claude-code/node_modules/@anthropic-ai/claude-code-linux-x64/claude /usr/bin/claude

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
