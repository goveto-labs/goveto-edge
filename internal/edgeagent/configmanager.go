package edgeagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/standard"

	"goveto-edge/internal/edgeprotocol"
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
	previousEncoded, err := renderCaddyConfig(m.sites, m.agentListen, m.agentHost)
	if err != nil {
		return fmt.Errorf("render previous site config: %w", err)
	}
	candidate := cloneSites(m.sites)
	candidate[config.SiteID] = config
	encoded, err := renderCaddyConfig(candidate, m.agentListen, m.agentHost)
	if err != nil {
		return fmt.Errorf("render site config: %w", err)
	}
	pending := m.path + ".pending"
	if err := persistSites(pending, candidate); err != nil {
		return fmt.Errorf("persist pending site config: %w", err)
	}
	if err := caddy.Load(encoded, true); err != nil {
		_ = os.Remove(pending)
		return fmt.Errorf("apply site config: %w", err)
	}
	if err := os.Rename(pending, m.path); err != nil {
		if rollbackErr := caddy.Load(previousEncoded, true); rollbackErr != nil {
			return fmt.Errorf("promote site config: %w; restore caddy config: %v", err, rollbackErr)
		}
		_ = os.Remove(pending)
		return fmt.Errorf("promote site config: %w", err)
	}
	m.sites = candidate
	return nil
}

// ConfigVersion is the highest applied site config version on this node.
func (m *ConfigManager) ConfigVersion() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var max uint64
	for _, site := range m.sites {
		if site.Version > max {
			max = site.Version
		}
	}
	return max
}

func (m *ConfigManager) SiteVersions() map[string]uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	versions := make(map[string]uint64, len(m.sites))
	for id, site := range m.sites {
		versions[id] = site.Version
	}
	return versions
}

func (m *ConfigManager) Stop() error { m.mu.Lock(); defer m.mu.Unlock(); return caddy.Stop() }

func (m *ConfigManager) Purge(ctx context.Context, purge edgeprotocol.PurgeRequest) error {
	m.mu.Lock()
	site, ok := m.sites[purge.SiteID]
	listen := m.agentListen
	m.mu.Unlock()
	if !ok || site.Disabled {
		return errors.New("site config is not active")
	}
	if site.Cache == nil {
		return errors.New("site cache is not enabled")
	}
	if len(site.Domains) == 0 {
		return errors.New("site has no domain")
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid agent listen address: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if !site.Listener.HTTPEnabled {
		scheme, port = "https", strconv.Itoa(site.Listener.HTTPSPort)
	}
	endpoint := scheme + "://" + net.JoinHostPort(host, port) + "/__goveto/cache/" + purge.SiteID
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	var request *http.Request
	if purge.Type == "ALL" {
		request, err = http.NewRequestWithContext(ctx, "PURGE", endpoint+"/flush", nil)
	} else {
		kind := map[string]string{"URL": "uri", "PREFIX": "uri-prefix", "TAG": "group"}[purge.Type]
		values := append([]string(nil), purge.Values...)
		if purge.Type != "TAG" {
			for i, value := range values {
				if strings.HasPrefix(value, "/") {
					values[i] = site.Domains[0] + value
				}
			}
		}
		payload := map[string]any{"type": kind, "purge": true}
		if purge.Type == "TAG" {
			payload["groups"] = values
		} else {
			payload["selectors"] = values
		}
		body, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return marshalErr
		}
		request, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if request != nil {
			request.Header.Set("Content-Type", "application/json")
		}
	}
	if err != nil {
		return err
	}
	request.Host = site.Domains[0]
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("cache purge rejected: %s", response.Status)
	}
	return nil
}

func persistSites(path string, sites map[string]SiteConfig) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(sites)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func cloneSites(source map[string]SiteConfig) map[string]SiteConfig {
	target := make(map[string]SiteConfig, len(source))
	for id, config := range source {
		target[id] = config
	}
	return target
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
		if site.Disabled {
			continue
		}
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
			configuration, _ := cache["Configuration"].(map[string]any)
			if configuration == nil {
				configuration = map[string]any{}
			}
			api, _ := configuration["API"].(map[string]any)
			if api == nil {
				api = map[string]any{}
			}
			api["souin"] = map[string]any{"enable": true, "basepath": "/__goveto/cache/" + id}
			configuration["API"] = api
			cache["Configuration"] = configuration
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
		if policy == "ip_hash" {
			policy = "client_ip_hash"
		}
		if _, err := caddy.GetModule("http.reverse_proxy.selection_policies." + policy); err != nil {
			return nil, fmt.Errorf("unsupported Caddy scheduler %q: %w", policy, err)
		}
		reverseProxy := map[string]any{"handler": "reverse_proxy", "upstreams": upstreams, "load_balancing": map[string]any{"selection_policy": map[string]any{"policy": policy}}}
		if strings.EqualFold(site.Origins[0].Protocol, "https") {
			reverseProxy["transport"] = map[string]any{"protocol": "http", "tls": map[string]any{}}
		}
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
