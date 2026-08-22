package edgeagent

import "goveto-edge/internal/buildinfo"

func currentAgentVersion() string {
	return buildinfo.Current()
}
