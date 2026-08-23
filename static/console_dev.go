//go:build !console_artifacts

package static

import (
	"embed"
	"errors"
	"io/fs"
)

// Keep development builds runnable from a clean checkout. The control plane
// serves the console only from a release build produced by build_control.sh.
//
//go:embed web/dist/.gitkeep
var consoleFiles embed.FS

const consoleEmbedded = false

// ErrConsoleUnavailable reports that the console SPA is not embedded.
var ErrConsoleUnavailable = errors.New("embedded console assets are unavailable")

// ConsoleFS returns the embedded console assets. It fails on development
// builds so callers can distinguish a missing SPA from an empty one.
func ConsoleFS() (fs.FS, error) {
	return nil, ErrConsoleUnavailable
}
