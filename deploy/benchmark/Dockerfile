FROM golang:1.26.5-bookworm AS build
ARG VCS_REF=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/edge-agent ./cmd/edge-agent \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-X goveto-edge/internal/agentbench.buildCommit=${VCS_REF}" -o /out/agent-bench ./cmd/agent-bench \
 && CGO_ENABLED=0 go build -trimpath -o /out/agent-bench-origin ./cmd/agent-bench-origin \
 && CGO_ENABLED=0 go build -trimpath -o /out/agent-bench-gateway ./cmd/agent-bench-gateway

FROM debian:bookworm-slim
RUN useradd --system --uid 10001 benchmark
COPY --from=build /out/* /usr/local/bin/
USER benchmark
