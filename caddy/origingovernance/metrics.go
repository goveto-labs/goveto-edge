package origingovernance

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
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
	siteID        string
	originAddress string
	requests      atomic.Uint64
	errors        atomic.Uint64
	latencyNanos  atomic.Uint64
	measuredAt    atomic.Int64
	healthy       atomic.Bool
	available     atomic.Bool
	fails         atomic.Int64
	upstream      atomic.Pointer[reverseproxy.Upstream]
}

var registry = struct {
	sync.Mutex
	values map[string]*metricState
}{values: map[string]*metricState{}}

var benchmarkObservabilityEnabled atomic.Bool

func init() {
	benchmarkObservabilityEnabled.Store(true)
	caddy.RegisterModule(MetricsHandler{})
}

// SetBenchmarkObservabilityEnabled is used only by the private benchmark listener.
func SetBenchmarkObservabilityEnabled(enabled bool) {
	benchmarkObservabilityEnabled.Store(enabled)
}

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
	state := registerMetric(siteID, address)
	state.healthy.Store(healthy)
	state.available.Store(available)
	state.fails.Store(int64(fails))
	state.measuredAt.Store(time.Now().UnixNano())
}

func registerMetric(siteID, address string) *metricState {
	registry.Lock()
	defer registry.Unlock()
	key := siteID + "\x00" + address
	state := registry.values[key]
	if state == nil {
		state = &metricState{siteID: siteID, originAddress: address}
		registry.values[key] = state
	}
	return state
}

func observe(siteID, address string, latency time.Duration, failed bool) {
	observeState(registerMetric(siteID, address), latency, failed)
}

func observeState(state *metricState, latency time.Duration, failed bool) {
	if state == nil {
		return
	}
	state.requests.Add(1)
	if failed {
		state.errors.Add(1)
	}
	state.latencyNanos.Add(uint64(max(latency, 0)))
	state.measuredAt.Store(time.Now().UnixNano())
}

func SnapshotAndReset() []Metric {
	registry.Lock()
	states := make([]*metricState, 0, len(registry.values))
	for _, state := range registry.values {
		states = append(states, state)
	}
	registry.Unlock()

	result := make([]Metric, 0, len(states))
	for _, state := range states {
		requests := state.requests.Swap(0)
		errors := state.errors.Swap(0)
		latency := state.latencyNanos.Swap(0)
		value := Metric{
			SiteID: state.siteID, OriginAddress: state.originAddress,
			Healthy: state.healthy.Load(), Available: state.available.Load(), Fails: int(state.fails.Load()),
			Requests: requests, Errors: errors,
		}
		if upstream := state.upstream.Load(); upstream != nil {
			value.Healthy = upstream.Healthy()
			value.Available = upstream.Available()
			value.Fails = upstream.Host.Fails()
		}
		if measuredAt := state.measuredAt.Load(); measuredAt > 0 {
			value.MeasuredAt = time.Unix(0, measuredAt).UTC()
		}
		if requests > 0 {
			value.AverageLatencyMS = float64(latency) / float64(time.Millisecond) / float64(requests)
			value.ErrorRate = float64(errors) / float64(requests)
		}
		result = append(result, value)
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
