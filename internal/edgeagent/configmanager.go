package edgeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

	"github.com/caddyserver/caddy/v2"
)

type ConfigManager struct {
	mu          sync.Mutex
	sites       map[string]SiteConfig
	path        string
	agentListen string
	agentHost   string
}

func (m *ConfigManager) SetAgentHost(host string) { m.mu.Lock(); m.agentHost = host; m.mu.Unlock() }

func NewConfigManager(path, agentListen string) *ConfigManager {
	manager := &ConfigManager{sites: map[string]SiteConfig{}}
	manager.path = path
	manager.agentListen = agentListen
	return manager
}

func (m *ConfigManager) Restore() error {
	if m.path == "" {
		return m.load(m.sites)
	}
	data, err := os.ReadFile(m.path)
	if os.IsNotExist(err) {
		return m.load(m.sites)
	}
	if err != nil {
		return err
	}
	var sites map[string]SiteConfig
	if err := json.Unmarshal(data, &sites); err != nil {
		return err
	}
	if err := m.load(sites); err != nil {
		return err
	}
	m.mu.Lock()
	m.sites = sites
	m.mu.Unlock()
	return nil
}

func (m *ConfigManager) load(sites map[string]SiteConfig) error {
	encoded, err := renderCaddyConfig(sites, m.agentListen, m.agentHost)
	if err != nil {
		return err
	}
	return caddy.Load(encoded, true)
}

func (m *ConfigManager) ApplySite(config SiteConfig) error {
	if err := config.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previous, existed := m.sites[config.SiteID]
	if existed && config.Version <= previous.Version {
		return errors.New("site config version is not newer")
	}
	m.sites[config.SiteID] = config
	encoded, err := renderCaddyConfig(m.sites, m.agentListen, m.agentHost)
	if err == nil {
		err = caddy.Load(encoded, true)
	}
	if err != nil {
		if existed {
			m.sites[config.SiteID] = previous
		} else {
			delete(m.sites, config.SiteID)
		}
		return fmt.Errorf("apply site config: %w", err)
	}
	if err := m.persistLocked(); err != nil {
		return fmt.Errorf("persist site config: %w", err)
	}
	return nil
}

func (m *ConfigManager) persistLocked() error {
	if m.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(m.sites)
	if err != nil {
		return err
	}
	temporary := m.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, m.path)
}

func renderCaddyConfig(sites map[string]SiteConfig, agentListen, agentHost string) ([]byte, error) {
	ids := make([]string, 0, len(sites))
	for id := range sites {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	routes := make([]any, 0, len(ids)*2+1)
	routes = append(routes, map[string]any{"@id": "goveto_agent_api", "match": []any{map[string]any{"host": []string{agentHost}}}, "handle": []any{map[string]any{"handler": "goveto_agent"}}, "terminal": true})
	listeners, protocols := map[string]struct{}{}, map[string]struct{}{"h1": {}}
	listeners[agentListen] = struct{}{}
	policies, certificates := make([]any, 0), make([]any, 0)
	for _, id := range ids {
		site := sites[id]
		listener := site.Listener
		if listener.HTTPEnabled {
			listeners[":"+strconv.Itoa(listener.HTTPPort)] = struct{}{}
		}
		if listener.HTTPSEnabled {
			listeners[":"+strconv.Itoa(listener.HTTPSPort)] = struct{}{}
			if listener.HTTP2Enabled {
				protocols["h2"] = struct{}{}
			}
			if listener.HTTP3Enabled {
				protocols["h3"] = struct{}{}
			}
		}
		if listener.RedirectHTTPToHTTPS {
			routes = append(routes, map[string]any{"@id": "site_" + id + "_redirect", "match": []any{map[string]any{"host": site.Domains, "protocol": "http"}}, "handle": []any{map[string]any{"handler": "static_response", "status_code": 301, "headers": map[string]any{"Location": []string{"https://{http.request.host}{http.request.uri}"}}}}, "terminal": true})
		}
		handlers := make([]any, 0, 4)
		if listener.HSTSEnabled {
			value := "max-age=" + strconv.Itoa(listener.HSTSMaxAge)
			if listener.HSTSIncludeSubdomains {
				value += "; includeSubDomains"
			}
			if listener.HSTSPreload {
				value += "; preload"
			}
			handlers = append(handlers, map[string]any{"handler": "headers", "response": map[string]any{"set": map[string][]string{"Strict-Transport-Security": {value}}}})
		}
		if site.Cache != nil {
			cache := cloneMap(site.Cache)
			cache["handler"] = "cache"
			handlers = append(handlers, cache)
		}
		upstreams := make([]any, 0, len(site.Origins))
		var hostHeader string
		for _, origin := range site.Origins {
			upstreams = append(upstreams, map[string]any{"dial": origin.Address})
			if hostHeader == "" {
				hostHeader = origin.HostHeader
			}
		}
		policy := site.Scheduler
		if policy == "" {
			policy = "round_robin"
		}
		reverseProxy := map[string]any{"handler": "reverse_proxy", "upstreams": upstreams, "load_balancing": map[string]any{"selection_policy": map[string]any{"policy": policy}}}
		if hostHeader != "" {
			reverseProxy["headers"] = map[string]any{"request": map[string]any{"set": map[string][]string{"Host": {hostHeader}}}}
		}
		handlers = append(handlers, reverseProxy)
		routes = append(routes, map[string]any{"@id": "site_" + id, "match": []any{map[string]any{"host": site.Domains}}, "handle": handlers, "terminal": true})
		if listener.HTTPSEnabled {
			minimum := "tls1.2"
			if listener.TLSMinVersion == "TLS1_3" {
				minimum = "tls1.3"
			}
			policies = append(policies, map[string]any{"match": map[string]any{"sni": site.Domains}, "protocol_min": minimum})
			for _, certificate := range site.Certificates {
				certificates = append(certificates, map[string]any{"certificate": certificate.CertificatePEM, "key": certificate.PrivateKeyPEM})
			}
		}
	}
	listen := keys(listeners)
	protocolList := keys(protocols)
	servers := map[string]any{"edge": map[string]any{"listen": listen, "protocols": protocolList, "routes": routes, "tls_connection_policies": policies, "logs": map[string]any{}}}
	config := map[string]any{"admin": map[string]any{"disabled": true}, "logging": map[string]any{"logs": map[string]any{"default": map[string]any{"writer": map[string]any{"output": "goveto_buffer"}}}}, "apps": map[string]any{"http": map[string]any{"servers": servers}, "tls": map[string]any{"certificates": map[string]any{"load_pem": certificates}}}}
	return json.Marshal(config)
}

func keys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
func cloneMap(source map[string]any) map[string]any {
	target := make(map[string]any, len(source)+1)
	for key, value := range source {
		target[key] = value
	}
	return target
}
