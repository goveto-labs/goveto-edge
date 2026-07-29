package edgeagent

import (
	"runtime/debug"
	"strings"
)

func currentAgentVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var revision string
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision != "" {
		if len(revision) > 12 {
			revision = revision[:12]
		}
		if modified {
			revision += "-dirty"
		}
		return revision
	}
	if version := strings.TrimSpace(info.Main.Version); version != "" && version != "(devel)" {
		return version
	}
	return "dev"
}
