package origingovernance

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
)

type MetricsHandler struct {
	SiteID  string         `json:"site_id"`
	Timeout caddy.Duration `json:"timeout,omitempty"`
}

type Metric struct {
	SiteID           string    `json:"site_id"`
	OriginAddress    string    `json:"origin_address"`
	Healthy          bool      `json:"healthy"`
	Available        bool      `json:"available"`
	Fails            int       `json:"fails"`
	Requests         uint64    `json:"requests"`
	Errors           uint64    `json:"errors"`
	AverageLatencyMS float64   `json:"average_latency_ms"`
	ErrorRate        float64   `json:"error_rate"`
	MeasuredAt       time.Time `json:"measured_at"`
}

type metricState struct {
	Metric
	latency  time.Duration
	upstream *reverseproxy.Upstream
}

var registry = struct {
	sync.Mutex
	values map[string]*metricState
}{values: map[string]*metricState{}}

func init() { caddy.RegisterModule(MetricsHandler{}) }

func (MetricsHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.goveto_origin_metrics", New: func() caddy.Module { return new(MetricsHandler) }}
}

func (h MetricsHandler) ServeHTTP(w http.ResponseWriter, request *http.Request, next caddyhttp.Handler) error {
	ctx := request.Context()
	var cancel context.CancelFunc
	if h.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(h.Timeout))
		defer cancel()
		request = request.Clone(ctx)
	}
	return next.ServeHTTP(w, request)
}

func updateHealth(siteID, address string, healthy, available bool, fails int) {
	registry.Lock()
	defer registry.Unlock()
	state := metric(siteID, address)
	state.Healthy = healthy
	state.Available = available
	state.Fails = fails
	state.MeasuredAt = time.Now().UTC()
}

func trackUpstream(siteID, address string, upstream *reverseproxy.Upstream) {
	registry.Lock()
	defer registry.Unlock()
	state := metric(siteID, address)
	state.upstream = upstream
	state.Healthy = upstream.Healthy()
	state.Available = upstream.Available()
	state.Fails = upstream.Host.Fails()
	state.MeasuredAt = time.Now().UTC()
}

func observe(siteID, address string, latency time.Duration, failed bool) {
	registry.Lock()
	defer registry.Unlock()
	state := metric(siteID, address)
	state.Requests++
	if failed {
		state.Errors++
	}
	state.latency += latency
	state.MeasuredAt = time.Now().UTC()
}

func metric(siteID, address string) *metricState {
	key := siteID + "\x00" + address
	state := registry.values[key]
	if state == nil {
		state = &metricState{Metric: Metric{SiteID: siteID, OriginAddress: address}}
		registry.values[key] = state
	}
	return state
}

func SnapshotAndReset() []Metric {
	registry.Lock()
	defer registry.Unlock()
	result := make([]Metric, 0, len(registry.values))
	for _, state := range registry.values {
		if state.upstream != nil {
			state.Healthy = state.upstream.Healthy()
			state.Available = state.upstream.Available()
			state.Fails = state.upstream.Host.Fails()
		}
		value := state.Metric
		if value.Requests > 0 {
			value.AverageLatencyMS = float64(state.latency.Microseconds()) / 1000 / float64(value.Requests)
			value.ErrorRate = float64(value.Errors) / float64(value.Requests)
		}
		result = append(result, value)
		state.Requests = 0
		state.Errors = 0
		state.latency = 0
	}
	return result
}

func ResetMetrics() {
	registry.Lock()
	defer registry.Unlock()
	registry.values = map[string]*metricState{}
}

var (
	_ caddy.Module                = (*MetricsHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*MetricsHandler)(nil)
)
