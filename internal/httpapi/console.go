package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/labstack/echo/v5"

	staticassets "goveto-edge/static"
)

// registerConsole wires the embedded console SPA as a history-fallback
// catch-all. Development builds without embedded artifacts skip registration
// and serve the API only; the Vite dev server provides the console instead.
func registerConsole(e *echo.Echo) {
	files, err := staticassets.ConsoleFS()
	if err != nil {
		slog.Info("console artifacts are not embedded; serving API only (run script/build_control.sh for a release build)")
		return
	}
	root, err := fs.Sub(files, "web/dist")
	if err != nil {
		slog.Error("open embedded console assets", "error", err)
		return
	}
	RegisterConsoleFS(e, root)
}

// RegisterConsoleFS serves a console build from root as a catch-all route.
// It is registered after every API route; more specific routes always win.
// Only GET and HEAD are handled so API method semantics (405, Allow) are
// unchanged.
func RegisterConsoleFS(e *echo.Echo, root fs.FS) {
	handler := consoleHandler{root: root}
	e.Match([]string{http.MethodGet, http.MethodHead}, "/*", handler.serve)
}

type consoleHandler struct {
	root fs.FS
}

// serve maps a request path onto the console build:
//
//   - reserved prefixes keep API semantics and answer JSON 404 instead of the
//     SPA shell, so unknown API paths stay machine-readable,
//   - files that exist in the build are served with immutable caching when
//     they live under the hashed assets/ directory,
//   - anything else (SPA deep links such as /sites/42) returns index.html so
//     client-side routing works on hard refresh.
func (h consoleHandler) serve(c *echo.Context) error {
	requested := c.Request().URL.Path
	if consoleReservedPath(requested) {
		return echo.NewHTTPError(http.StatusNotFound, http.StatusText(http.StatusNotFound))
	}
	if name := consoleFileName(requested); name != "" {
		if info, err := fs.Stat(h.root, name); err == nil && !info.IsDir() {
			c.Response().Header().Set("Cache-Control", consoleCacheControl(name))
			return c.FileFS(name, h.root)
		}
		// A missing hashed asset must not fall back to HTML: browsers and
		// service workers fetching stale asset URLs need a real 404.
		if strings.HasPrefix(name, "assets/") {
			return echo.NewHTTPError(http.StatusNotFound, http.StatusText(http.StatusNotFound))
		}
	}
	// History fallback: the SPA decides how to render unknown routes.
	c.Response().Header().Set("Cache-Control", "no-cache")
	return c.FileFS("index.html", h.root)
}

// consoleReservedPrefixes are never answered with the SPA shell. Unmatched
// paths below them fall through to the JSON error handler.
var consoleReservedPrefixes = []string{"/api", "/health", "/metrics"}

func consoleReservedPath(requested string) bool {
	for _, prefix := range consoleReservedPrefixes {
		if requested == prefix || strings.HasPrefix(requested, prefix+"/") {
			return true
		}
	}
	return false
}

// consoleFileName normalizes a request path to a build file name. It returns
// "" for dot files and the root document so those are served by the fallback.
func consoleFileName(requested string) string {
	name := strings.TrimPrefix(path.Clean("/"+requested), "/")
	if name == "" || name == "." || strings.HasPrefix(path.Base(name), ".") {
		return ""
	}
	return name
}

func consoleCacheControl(name string) string {
	if strings.HasPrefix(name, "assets/") {
		// Vite content-hashes every asset filename; the bytes behind a name
		// never change, so browsers may cache them for the release lifetime.
		return "public, max-age=31536000, immutable"
	}
	// index.html and other root documents reference the current asset hashes
	// and must be revalidated after every upgrade.
	return "no-cache"
}
