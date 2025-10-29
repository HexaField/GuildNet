FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . /src
RUN cd internal/agent/collector && go build -o /bin/agent

FROM alpine:3.18
COPY --from=build /bin/agent /bin/agent
ENTRYPOINT ["/bin/agent"]
# Multi-stage build for a lightweight agent image running code-server behind Caddy

########## Builder: fetch code-server and Caddy ##########
FROM debian:bookworm-slim AS builder

ARG DEBIAN_FRONTEND=noninteractive
ARG CODE_SERVER_VERSION=latest
ARG TARGETARCH

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends curl ca-certificates jq xz-utils tar; \
    rm -rf /var/lib/apt/lists/*

# Resolve code-server version and download
RUN set -eux; \
        mkdir -p /tmp/code-server; \
        ARCH="${TARGETARCH}"; \
        case "$ARCH" in \
            amd64) CS_ARCH=linux-amd64 ;; \
            arm64) CS_ARCH=linux-arm64 ;; \
            *) echo "Unsupported TARGETARCH: $ARCH"; exit 1 ;; \
        esac; \
    if [ "$CODE_SERVER_VERSION" = "latest" ]; then \
      VER=$(curl -fsSL https://api.github.com/repos/coder/code-server/releases/latest | jq -r .tag_name | sed 's/^v//'); \
    else \
      VER="$CODE_SERVER_VERSION"; \
    fi; \
    echo "Using code-server version: ${VER}"; \
        echo "Detected TARGETARCH=${TARGETARCH}, downloading ${CS_ARCH}"; \
        curl -fsSL "https://github.com/coder/code-server/releases/download/v${VER}/code-server-${VER}-${CS_ARCH}.tar.gz" -o /tmp/code-server.tgz; \
    tar -xzf /tmp/code-server.tgz -C /tmp/code-server --strip-components=1; \
        true

# Install tini in builder to copy into final image
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends tini; \
    rm -rf /var/lib/apt/lists/*

########## Final runtime image ##########
FROM debian:bookworm-slim

ARG DEBIAN_FRONTEND=noninteractive

# Install minimal runtime deps, including Caddy
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl caddy; \
    rm -rf /var/lib/apt/lists/*

# Create non-root user and dirs
RUN set -eux; \
    useradd -u 10001 -m -d /home/app -s /usr/sbin/nologin app; \
    install -d -m 0755 -o app -g app /opt/agent /srv /workspace /data

# Copy code-server distribution and install symlink so relative paths work
COPY --from=builder /tmp/code-server /usr/local/lib/code-server
RUN ln -sf /usr/local/lib/code-server/bin/code-server /usr/local/bin/code-server
COPY --from=builder /usr/bin/tini /sbin/tini

# Copy agent assets
COPY start.sh /opt/agent/start.sh
COPY Caddyfile /opt/agent/Caddyfile
COPY index.html /srv/index.html

RUN set -eux; \
    chmod +x /opt/agent/start.sh; \
    chown -R app:app /opt/agent /srv /workspace /data

ENV PORT=8080 \
    HTTPS_PORT=8443 \
    HOME=/home/app

EXPOSE 8080 8443

# Healthcheck for Kubernetes/docker
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD sh -c "curl -fsS http://127.0.0.1:${PORT:-8080}/healthz >/dev/null || exit 1"

USER app

WORKDIR /workspace

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/opt/agent/start.sh"]
