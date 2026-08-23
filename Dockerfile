# syntax=docker/dockerfile:1

# Production image for the Goveto Edge control plane. The console SPA and both
# edge-agent architectures are embedded into the binary, matching what
# script/build_control.sh produces locally and in CI.

ARG GO_VERSION=1.26.5
ARG NODE_VERSION=24
ARG GOVETO_VERSION=dev

# --- console -----------------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-bookworm-slim AS console
WORKDIR /src
# packageManager pins the exact pnpm version via corepack.
RUN corepack enable
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY frontend/ ./
RUN pnpm run build

# --- edge agents ---------------------------------------------------------------
# Both linux architectures are cross-compiled so a single control-plane image
# embeds agents for amd64 and arm64 edge nodes regardless of its own platform.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS agents
ARG GOVETO_VERSION
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN script/build_agent.sh --version "$GOVETO_VERSION"

# --- control plane -------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build
ARG GOVETO_VERSION
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=agents /src/static/agent/ static/agent/
COPY --from=console /src/dist/ static/web/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -tags agent_artifacts,console_artifacts -trimpath \
    -ldflags="-X goveto-edge/internal/buildinfo.Version=$GOVETO_VERSION" \
    -o /out/control-api ./cmd/control-api \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath -o /out/healthprobe ./cmd/healthprobe \
 && mkdir -p /out/data

# --- runtime --------------------------------------------------------------------
# distroless/static ships CA certificates and tzdata without a shell or
# package manager; healthprobe stands in for curl in HEALTHCHECK.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/control-api /usr/local/bin/control-api
COPY --from=build /out/healthprobe /usr/local/bin/healthprobe
# The data directory must be writable by the non-root user; named volumes
# inherit ownership from the image on first use.
COPY --from=build --chown=nonroot:nonroot /out/data /var/lib/goveto-edge
ENV GOVETO_DATA_DIR=/var/lib/goveto-edge \
    APP_ENV=production
EXPOSE 8080 8443
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD ["/usr/local/bin/healthprobe", "-url", "http://127.0.0.1:8080/health/ready"]
ENTRYPOINT ["/usr/local/bin/control-api"]
