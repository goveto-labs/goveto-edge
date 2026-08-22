// Package buildinfo exposes the version shared by the control plane and edge agent.
package buildinfo

import (
	"fmt"
	"io"
	"strings"

	"golang.org/x/mod/semver"
)

// Version is replaced by release builds with -ldflags -X.
var Version = "dev"

func Current() string {
	value := strings.TrimSpace(Version)
	if value == "" || value == "(devel)" {
		return "dev"
	}
	if value != "dev" && !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	return value
}

func IsRelease() bool { return semver.IsValid(Current()) }

// NeedsUpgrade reports whether a release target should replace the reported
// agent version. Legacy non-SemVer agent versions are treated as old, while a
// newer agent is never downgraded during a control-plane rollback.
func NeedsUpgrade(current, target string) bool {
	if !semver.IsValid(target) {
		return false
	}
	if !strings.HasPrefix(current, "v") {
		current = "v" + current
	}
	if !semver.IsValid(current) {
		return true
	}
	return semver.Compare(current, target) < 0
}

// PrintCommand handles the common `version` and `--version` command forms.
func PrintCommand(args []string, output io.Writer, program string) bool {
	if len(args) != 1 || (args[0] != "version" && args[0] != "--version") {
		return false
	}
	_, _ = fmt.Fprintf(output, "%s %s\n", program, Current())
	return true
}
