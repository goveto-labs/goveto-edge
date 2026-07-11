#!/bin/bash
set -e

rm -f static/agent/agent-linux-amd64
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o static/agent/agent-linux-amd64 cmd/edge-agent/main.go
upx static/agent/agent-linux-amd64

rm -f static/agent/agent-linux-arm64
GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o static/agent/agent-linux-arm64 cmd/edge-agent/main.go
upx static/agent/agent-linux-arm64