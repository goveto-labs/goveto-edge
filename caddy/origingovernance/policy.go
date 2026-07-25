package origingovernance

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
)

type Backend struct {
	Dial       string `json:"dial"`
	HostHeader string `json:"host_header,omitempty"`
	Weight     int    `json:"weight,omitempty"`
	Priority   int    `json:"priority,omitempty"`
}

type SelectionPolicy struct {
	SiteID    string    `json:"site_id"`
	Scheduler string    `json:"scheduler,omitempty"`
	Backends  []Backend `json:"backends"`

	index  atomic.Uint64
	byDial map[string]Backend
}

func init() { caddy.RegisterModule(SelectionPolicy{}) }

func (SelectionPolicy) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.reverse_proxy.selection_policies.goveto_origin",
		New: func() caddy.Module { return new(SelectionPolicy) },
	}
}

func (p *SelectionPolicy) Provision(caddy.Context) error {
	if p.SiteID == "" || len(p.Backends) == 0 {
		return fmt.Errorf("site_id and backends are required")
	}
	switch p.Scheduler {
	case "", "round_robin", "weighted_round_robin", "least_conn", "random", "first", "ip_hash", "client_ip_hash":
	default:
		return fmt.Errorf("unsupported scheduler %q", p.Scheduler)
	}
	p.byDial = make(map[string]Backend, len(p.Backends))
	for _, backend := range p.Backends {
		if backend.Dial == "" || backend.Priority < 0 || backend.Weight < 0 {
			return fmt.Errorf("invalid origin backend %#v", backend)
		}
		if backend.Weight == 0 {
			backend.Weight = 1
		}
		p.byDial[backend.Dial] = backend
	}
	return nil
}

func (p *SelectionPolicy) Select(pool reverseproxy.UpstreamPool, request *http.Request, _ http.ResponseWriter) *reverseproxy.Upstream {
	lowestPriority := int(^uint(0) >> 1)
	candidates := make([]*reverseproxy.Upstream, 0, len(pool))
	for _, upstream := range pool {
		backend, ok := p.byDial[upstream.Dial]
		if !ok {
			backend = Backend{Dial: upstream.Dial, Weight: 1}
		}
		available := upstream.Available()
		trackUpstream(p.SiteID, backend.Dial, upstream)
		if !available {
			continue
		}
		if backend.Priority < lowestPriority {
			lowestPriority = backend.Priority
			candidates = candidates[:0]
		}
		if backend.Priority == lowestPriority {
			candidates = append(candidates, upstream)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	selected := p.choose(candidates, request)
	backend := p.byDial[selected.Dial]
	host := backend.HostHeader
	if host == "" {
		host = dialHostPort(selected.Dial)
	}
	if replacer, ok := request.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer); ok {
		replacer.Set("goveto.origin.address", backend.Dial)
		replacer.Set("goveto.origin.host", host)
	}
	return selected
}

func (p *SelectionPolicy) choose(candidates reverseproxy.UpstreamPool, request *http.Request) *reverseproxy.Upstream {
	switch p.Scheduler {
	case "first":
		return candidates[0]
	case "least_conn":
		selected := candidates[0]
		for _, candidate := range candidates[1:] {
			if candidate.Host.NumRequests() < selected.Host.NumRequests() {
				selected = candidate
			}
		}
		return selected
	case "random":
		return candidates[rand.IntN(len(candidates))]
	case "ip_hash", "client_ip_hash":
		host, _, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil {
			host = request.RemoteAddr
		}
		hash := fnv.New64a()
		_, _ = hash.Write([]byte(host))
		return candidates[int(hash.Sum64()%uint64(len(candidates)))]
	default:
		return p.chooseWeighted(candidates)
	}
}

func (p *SelectionPolicy) chooseWeighted(candidates reverseproxy.UpstreamPool) *reverseproxy.Upstream {
	total := 0
	for _, candidate := range candidates {
		total += p.byDial[candidate.Dial].Weight
	}
	if total <= 0 {
		return candidates[0]
	}
	position := int((p.index.Add(1) - 1) % uint64(total))
	for _, candidate := range candidates {
		position -= p.byDial[candidate.Dial].Weight
		if position < 0 {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

func dialHostPort(dial string) string {
	if slash := strings.IndexByte(dial, '/'); slash >= 0 {
		dial = dial[slash+1:]
	}
	return dial
}

var (
	_ caddy.Module          = (*SelectionPolicy)(nil)
	_ caddy.Provisioner     = (*SelectionPolicy)(nil)
	_ reverseproxy.Selector = (*SelectionPolicy)(nil)
)
