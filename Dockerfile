# ── Frontend ────────────────────────────────────────────────────────────────
FROM node:22-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ── Server ──────────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/
COPY --from=frontend /src/web/dist web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /hoist ./cmd/hoist

# ── Runtime: the binary, git, and docker compose ────────────────────────────
FROM alpine:3.22
# Keep this in step with the host's `docker compose version`: the config hash
# that decides whether a container is recreated can change between versions.
ARG COMPOSE_VERSION=5.5.1
ARG TARGETARCH
RUN apk add --no-cache git openssh-client ca-certificates tzdata \
    && case "${TARGETARCH:-amd64}" in amd64) arch=x86_64 ;; arm64) arch=aarch64 ;; *) arch="$TARGETARCH" ;; esac \
    && url="https://github.com/docker/compose/releases/download/v${COMPOSE_VERSION}/docker-compose-linux-${arch}" \
    && wget -qO /usr/local/bin/docker-compose "$url" \
    && wget -qO /tmp/compose.sha256 "$url.sha256" \
    && echo "$(cut -d' ' -f1 /tmp/compose.sha256)  /usr/local/bin/docker-compose" | sha256sum -c - \
    && chmod +x /usr/local/bin/docker-compose \
    && rm /tmp/compose.sha256 \
    # The stack folders belong to the host user; don't let git balk at that.
    && git config --system safe.directory '*' \
    # ssh won't run for a uid without a passwd entry; 1000 is the usual one.
    && adduser -D -H -u 1000 -h /tmp -s /sbin/nologin hoist \
    && mkdir -p /config && chown 1000:1000 /config
COPY --from=server /hoist /hoist
ENV HOIST_CONFIG_DIR=/config \
    HOIST_PORT=8080 \
    HOME=/tmp \
    GOMEMLIMIT=32MiB
# Run as the user that owns your stack folders (see docker-compose.example.yml).
USER 1000:1000
EXPOSE 8080
VOLUME ["/config"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD ["/hoist", "healthcheck"]
ENTRYPOINT ["/hoist"]
