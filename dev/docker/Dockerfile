FROM ubuntu:22.04

# systemd inside container
ENV container=docker
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    systemd systemd-sysv \
    curl wget git sudo \
    ca-certificates gnupg \
    make build-essential \
    gawk \
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

# Agent CLIs — pinned versions via official curl installers, no Node/npm
ARG CLAUDE_CODE_VERSION=2.1.145
ARG CODEX_VERSION=0.135.0

# Claude Code — official binary installer
RUN curl -fsSL https://claude.ai/install.sh -o /tmp/install-claude.sh \
    && CLAUDE_CODE_VERSION=${CLAUDE_CODE_VERSION} bash /tmp/install-claude.sh \
    && rm /tmp/install-claude.sh

# Codex — official OpenAI curl installer; CODEX_RELEASE pins the version
RUN curl -fsSL https://chatgpt.com/codex/install.sh -o /tmp/install-codex.sh \
    && CODEX_NON_INTERACTIVE=1 CODEX_RELEASE=${CODEX_VERSION} sh /tmp/install-codex.sh \
    && rm /tmp/install-codex.sh

# Agy (Antigravity / Gemini CLI by Google) — floating latest; bump image to update
RUN curl -fsSL https://antigravity.google/cli/install.sh | bash \
    && ln -sf /root/.local/bin/agy /usr/local/bin/agy

# ── build omni binaries into image ────────────────────────────────────────────
WORKDIR /build

COPY go.work          go.work
COPY go.work.sum      go.work.sum
COPY omni/            omni/
COPY svc/             svc/
COPY pkg/             pkg/
COPY axolink/ axolink/
COPY dev/     dev/
COPY deployment/      deployment/

ARG VERSION=dev
ENV VERSION=${VERSION}
ENV OMNI_PREFIX=/opt/omni
# Module cache is mounted across builds — no re-download unless go.mod/go.sum change
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    OUT_DIR=/opt/omni/bin bash dev/build.sh

# ── runtime image setup ───────────────────────────────────────────────────────
ENV WORKSPACE=/build
WORKDIR /build

COPY dev/docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

VOLUME ["/var/lib/omni-root", "/root/.config"]

ENTRYPOINT ["/entrypoint.sh"]
