//go:build !agent_artifacts

package static

import "embed"

// Keep development builds runnable from a clean checkout. Agent installation
// and automatic upgrades require a release build produced by build_control.sh.
//
//go:embed agent/.gitkeep
var artifactFiles embed.FS

const agentArtifactsEmbedded = false
