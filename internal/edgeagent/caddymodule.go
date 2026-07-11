package edgeagent

import (
	"errors"
	"net/http"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

var agentHandlerState struct {
	sync.RWMutex
	handler http.Handler
}

func init() { caddy.RegisterModule(AgentAPIHandler{}) }

func setAgentHTTPHandler(handler http.Handler) {
	agentHandlerState.Lock()
	agentHandlerState.handler = handler
	agentHandlerState.Unlock()
}

type AgentAPIHandler struct{}

func (AgentAPIHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.goveto_agent", New: func() caddy.Module { return new(AgentAPIHandler) }}
}
func (AgentAPIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	agentHandlerState.RLock()
	handler := agentHandlerState.handler
	agentHandlerState.RUnlock()
	if handler == nil {
		return errors.New("agent API is not initialized")
	}
	handler.ServeHTTP(w, r)
	return nil
}
func (*AgentAPIHandler) Provision(caddy.Context) error { return nil }
func (*AgentAPIHandler) Validate() error               { return nil }

var _ caddyhttp.MiddlewareHandler = (*AgentAPIHandler)(nil)
