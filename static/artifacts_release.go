//go:build agent_artifacts

package static

import "embed"

// List release inputs explicitly so unrelated build-directory files cannot be
// included in the control-plane binary.
//
//go:embed agent/manifest.json agent/agent-linux-amd64 agent/agent-linux-arm64
var artifactFiles embed.FS

const agentArtifactsEmbedded = true
