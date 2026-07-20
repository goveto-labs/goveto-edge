package cachepurge

import (
	"net/http"
	"strconv"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	cachefs "goveto-edge/caddy/simplefs"
)

type Handler struct {
	Path  string   `json:"path"`
	Hosts []string `json:"hosts"`
}

func init() { caddy.RegisterModule(Handler{}) }

func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.goveto_cache_purge", New: func() caddy.Module { return new(Handler) }}
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	count, err := cachefs.Purge(h.Path, "URL", h.Hosts, []string{r.URL.RequestURI()})
	if err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}
	w.Header().Set("X-Goveto-Purged-Objects", strconv.Itoa(count))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

var _ caddyhttp.MiddlewareHandler = (*Handler)(nil)
