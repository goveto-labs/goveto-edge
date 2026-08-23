//go:build console_artifacts

package static

import (
	"embed"
	"errors"
	"io/fs"
)

// Console holds the built console SPA served by the control plane. The
// contents are produced by script/build_frontend.sh and explicitly listed so
// unrelated build-directory files cannot be included.
//
//go:embed all:web/dist
var consoleFiles embed.FS

const consoleEmbedded = true

// ErrConsoleUnavailable reports that the console SPA is not embedded.
var ErrConsoleUnavailable = errors.New("embedded console artifacts are unavailable")

// ConsoleFS returns the embedded console assets rooted at the build output.
// The returned filesystem contains index.html and hashed asset files.
func ConsoleFS() (fs.FS, error) {
	return consoleFiles, nil
}
