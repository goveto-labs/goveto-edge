package edgeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/standard"

	_ "goveto-edge/caddy/compression"
	cachefs "goveto-edge/caddy/simplefs"
	"goveto-edge/internal/edgeprotocol"
	cachepolicy "goveto-edge/internal/policy"
)

type ConfigManager struct {
	mu            sync.Mutex
	sites         map[string]SiteConfig
	path          string
	defaultListen string
	nodeConfig    NodeConfig
}

func NewConfigManager(path, defaultListen string) *ConfigManager {
	manager := &ConfigManager{
		sites: map[string]SiteConfig{},
		nodeConfig: NodeConfig{
			CacheDirectory:      "/opt/goveto-edge/cache",
			AutoMaxSize:         true,
			MaxDiskUsagePercent: 80,
		},
	}
	manager.path = path
	manager.defaultListen = defaultListen
	return manager
}

func (m *ConfigManager) SetNodeConfig(config NodeConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	encoded, err := renderCaddyConfig(m.sites, m.defaultListen, "", config)
	if err != nil {
		return err
	}
	if err = caddy.Load(encoded, true); err != nil {
		return err
	}
	m.nodeConfig = config
	return nil
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
	encoded, err := renderCaddyConfig(sites, m.defaultListen, "", m.nodeConfig)
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

	previousEncoded, err := renderCaddyConfig(m.sites, m.defaultListen, "", m.nodeConfig)
	if err != nil {
		return fmt.Errorf("render previous site config: %w", err)
	}

	candidate := cloneSites(m.sites)
	candidate[config.SiteID] = config

	encoded, err := renderCaddyConfig(candidate, m.defaultListen, "", m.nodeConfig)
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

func (m *ConfigManager) Purge(_ context.Context, purge edgeprotocol.PurgeRequest) error {
	m.mu.Lock()
	site, ok := m.sites[purge.SiteID]
	cacheDirectory := m.nodeConfig.CacheDirectory
	m.mu.Unlock()

	if !ok || site.Disabled {
		return errors.New("site config is not active")
	}
	cachePolicy, configured, err := decodeCachePolicy(site.Cache)
	if err != nil {
		return fmt.Errorf("invalid site cache policy: %w", err)
	}
	if !configured || !cachePolicy.Enabled {
		return errors.New("site cache is not enabled")
	}
	if len(site.Domains) == 0 {
		return errors.New("site has no domain")
	}
	_, err = cachefs.Purge(
		filepath.Join(cacheDirectory, site.SiteID),
		purge.Type,
		site.Domains,
		purge.Values,
	)
	return err
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

func renderCaddyConfig(sites map[string]SiteConfig, defaultListen, _ string, nodeConfigs ...NodeConfig) ([]byte, error) {
	nodeConfig := NodeConfig{
		CacheDirectory:      "/opt/goveto-edge/cache",
		AutoMaxSize:         true,
		MaxDiskUsagePercent: 80,
	}
	if len(nodeConfigs) > 0 {
		nodeConfig = nodeConfigs[0]
	}

	ids := make([]string, 0, len(sites))
	for id := range sites {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	routes := make([]any, 0, len(ids)*2)

	listeners, protocols := map[string]struct{}{}, map[string]struct{}{"h1": {}}
	listeners[defaultListen] = struct{}{}
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
			routes = append(routes, map[string]any{
				"@id": "site_" + id + "_redirect",
				"match": []any{
					map[string]any{
						"host":     site.Domains,
						"protocol": "http",
					},
				},
				"handle": []any{
					map[string]any{
						"handler":     "static_response",
						"status_code": 301,
						"headers": map[string]any{
							"Location": []string{"https://{http.request.host}{http.request.uri}"},
						},
					},
				},
				"terminal": true,
			})
		}

		handlers := make([]any, 0, 6)
		wafPolicy, wafConfigured, err := decodeWAFPolicy(site.WAF)
		if err != nil {
			return nil, fmt.Errorf("site %s WAF policy: %w", id, err)
		}
		rateLimitPolicy, rateLimitConfigured, err := decodeRateLimitPolicy(site.RateLimit)
		if err != nil {
			return nil, fmt.Errorf("site %s rate-limit policy: %w", id, err)
		}
		if (wafConfigured && wafPolicy.Enabled) || (rateLimitConfigured && rateLimitPolicy.Enabled) {
			securityHandler := map[string]any{
				"handler":    "goveto_waf",
				"site_id":    id,
				"waf":        wafPolicy,
				"rate_limit": rateLimitPolicy,
			}
			if secret := stringMapValue(site.WAF, "challenge_secret"); secret != "" {
				securityHandler["challenge_secret"] = secret
			}
			handlers = append(handlers, securityHandler)
		}
		if listener.HSTSEnabled {
			value := "max-age=" + strconv.Itoa(listener.HSTSMaxAge)
			if listener.HSTSIncludeSubdomains {
				value += "; includeSubDomains"
			}
			if listener.HSTSPreload {
				value += "; preload"
			}
			handlers = append(handlers, map[string]any{
				"handler": "headers",
				"response": map[string]any{
					"set": map[string][]string{
						"Strict-Transport-Security": {value},
					},
				},
			})
		}
		compressionPolicy, compressionConfigured, err := decodeCompressionPolicy(site.Compression)
		if err != nil {
			return nil, fmt.Errorf("site %s compression policy: %w", id, err)
		}
		if compressionConfigured && compressionPolicy.Enabled {
			handlers = append(handlers, map[string]any{
				"handler":             "goveto_compression",
				"extensions":          compressionPolicy.Extensions,
				"excluded_extensions": compressionPolicy.ExcludedExtensions,
				"mime_types":          compressionPolicy.MIMETypes,
				"recompress":          compressionPolicy.Recompress,
				"minimum_length":      compressionPolicy.MinimumLength,
				"maximum_length":      compressionPolicy.MaximumLength,
				"excluded_paths":      compressionPolicy.ExcludedPaths,
			})
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

		reverseProxy := map[string]any{
			"handler":   "reverse_proxy",
			"upstreams": upstreams,
			"load_balancing": map[string]any{
				"selection_policy": map[string]any{"policy": policy},
			},
		}
		if strings.EqualFold(site.Origins[0].Protocol, "https") {
			reverseProxy["transport"] = map[string]any{
				"protocol": "http",
				"tls":      map[string]any{},
			}
		}
		if hostHeader != "" {
			reverseProxy["headers"] = map[string]any{
				"request": map[string]any{
					"set": map[string][]string{"Host": {hostHeader}},
				},
			}
		}

		if cachePolicy, ok, err := decodeCachePolicy(site.Cache); err != nil {
			return nil, fmt.Errorf("site %s cache policy: %w", id, err)
		} else if ok && cachePolicy.Enabled {
			cacheHandler := souinHandler(id, cachePolicy, nodeConfig)
			routes = append(routes, map[string]any{
				"@id": "site_" + id + "_cache_api",
				"match": []any{
					map[string]any{
						"host":   site.Domains,
						"method": []string{http.MethodPost, "PURGE"},
						"path":   []string{"/__goveto/cache/" + id + "*"},
					},
				},
				"handle":   []any{cacheHandler},
				"terminal": true,
			})
			cachedHandlers := append([]any(nil), handlers...)
			cachedHandlers = append(cachedHandlers, map[string]any{
				"handler": "goveto_cache_headers",
				"x_cache": cachePolicy.ResponseHeaders.XCache,
				"age":     cachePolicy.ResponseHeaders.Age,
			})
			cachedHandlers = append(cachedHandlers,
				cacheHandler,
				map[string]any{
					"handler":            "goveto_cache_headers",
					"age":                true,
					"default_ttl":        cachePolicy.TTL.DefaultSeconds,
					"status_ttl":         cachePolicy.TTL.Status,
					"stale_if_error_ttl": staleIfErrorTTL(cachePolicy),
				},
				reverseProxy,
			)

			methods := []string{http.MethodGet, http.MethodHead}
			if cachePolicy.AllowPurgeMethod {
				routes = append(routes, map[string]any{
					"@id": "site_" + id + "_purge",
					"match": []any{
						map[string]any{
							"host":   site.Domains,
							"method": []string{"PURGE"},
						},
					},
					"handle": []any{
						map[string]any{
							"handler": "goveto_cache_purge",
							"path":    filepath.Join(nodeConfig.CacheDirectory, id),
							"hosts":   site.Domains,
						},
					},
					"terminal": true,
				})
			}
			routes = append(routes, map[string]any{
				"@id": "site_" + id + "_cache",
				"match": []any{
					map[string]any{
						"host":   site.Domains,
						"method": methods,
						"goveto_cache": map[string]any{
							"conditions": cachePolicy.Conditions,
						},
					},
				},
				"handle":   cachedHandlers,
				"terminal": true,
			})
		}

		handlers = append(handlers, reverseProxy)
		routes = append(routes, map[string]any{
			"@id": "site_" + id,
			"match": []any{
				map[string]any{"host": site.Domains},
			},
			"handle":   handlers,
			"terminal": true,
		})

		if listener.HTTPSEnabled {
			minimum := "tls1.2"
			if listener.TLSMinVersion == "TLS1_3" {
				minimum = "tls1.3"
			}
			policies = append(policies, map[string]any{
				"match": map[string]any{
					"sni": site.Domains,
				},
				"protocol_min": minimum,
			})
			for _, certificate := range site.Certificates {
				certificates = append(certificates, map[string]any{
					"certificate": certificate.CertificatePEM,
					"key":         certificate.PrivateKeyPEM,
				})
			}
		}
	}
	routes = append(routes, map[string]any{
		"@id": "goveto_unmatched_host",
		"handle": []any{
			map[string]any{
				"handler":     "static_response",
				"status_code": http.StatusNotFound,
				"body":        "site host not configured\n",
			},
		},
		"terminal": true,
	})

	listen := keys(listeners)
	protocolList := keys(protocols)
	servers := map[string]any{
		"edge": map[string]any{
			"listen":                  listen,
			"protocols":               protocolList,
			"routes":                  routes,
			"tls_connection_policies": policies,
			"automatic_https":         map[string]any{"disable": true},
			"logs":                    map[string]any{},
		},
	}
	config := map[string]any{
		"admin": map[string]any{"disabled": true},
		"logging": map[string]any{
			"logs": map[string]any{
				"default": map[string]any{
					"writer": map[string]any{"output": "goveto_buffer"},
				},
			},
		},
		"apps": map[string]any{
			"http": map[string]any{
				"servers": servers,
			},
			"tls": map[string]any{
				"certificates": map[string]any{
					"load_pem": certificates,
				},
			},
		},
	}
	return json.Marshal(config)
}

func decodeCachePolicy(raw map[string]any) (cachepolicy.CachePolicy, bool, error) {
	if raw == nil {
		return cachepolicy.CachePolicy{}, false, nil
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return cachepolicy.CachePolicy{}, false, err
	}

	policy := cachepolicy.DefaultCachePolicy()
	if err = json.Unmarshal(data, &policy); err != nil {
		return policy, false, err
	}
	if err = policy.NormalizeAndValidate(); err != nil {
		return policy, false, err
	}
	return policy, true, nil
}

func decodeCompressionPolicy(raw map[string]any) (cachepolicy.CompressionPolicy, bool, error) {
	if raw == nil {
		return cachepolicy.CompressionPolicy{}, false, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return cachepolicy.CompressionPolicy{}, false, err
	}
	policy := cachepolicy.DefaultCompressionPolicy()
	if err = json.Unmarshal(data, &policy); err != nil {
		return policy, false, err
	}
	if err = policy.NormalizeAndValidate(); err != nil {
		return policy, false, err
	}
	return policy, true, nil
}

func decodeWAFPolicy(raw map[string]any) (cachepolicy.WAFPolicy, bool, error) {
	if raw == nil {
		return cachepolicy.WAFPolicy{}, false, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return cachepolicy.WAFPolicy{}, false, err
	}
	policy := cachepolicy.DefaultWAFPolicy()
	if err = json.Unmarshal(data, &policy); err != nil {
		return policy, false, err
	}
	if err = policy.NormalizeAndValidate(); err != nil {
		return policy, false, err
	}
	return policy, true, nil
}

func decodeRateLimitPolicy(raw map[string]any) (cachepolicy.RateLimitPolicy, bool, error) {
	if raw == nil {
		return cachepolicy.RateLimitPolicy{}, false, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return cachepolicy.RateLimitPolicy{}, false, err
	}
	policy := cachepolicy.DefaultRateLimitPolicy()
	if err = json.Unmarshal(data, &policy); err != nil {
		return policy, false, err
	}
	if err = policy.NormalizeAndValidate(); err != nil {
		return policy, false, err
	}
	return policy, true, nil
}

func souinHandler(siteID string, policy cachepolicy.CachePolicy, nodeConfig NodeConfig) map[string]any {
	stale := "0s"
	if policy.Stale.Enabled {
		stale = strconv.Itoa(policy.Stale.IfErrorSeconds) + "s"
	}

	verbs := []string{http.MethodGet, http.MethodHead}
	if policy.AllowPurgeMethod {
		verbs = append(verbs, "PURGE")
	}

	configuration := map[string]any{
		"DefaultCache": map[string]any{
			"allowed_http_verbs":    verbs,
			"cache_name":            "Goveto-" + siteID,
			"key":                   map[string]any{"headers": policy.VaryHeaders},
			"ttl":                   strconv.Itoa(policy.TTL.DefaultSeconds) + "s",
			"stale":                 stale,
			"default_cache_control": "public, max-age=" + strconv.Itoa(policy.TTL.DefaultSeconds),
			"simplefs": map[string]any{
				"found": true,
				"path":  filepath.Join(nodeConfig.CacheDirectory, siteID),
				"configuration": map[string]any{
					"auto_max_size":          nodeConfig.AutoMaxSize,
					"max_size_bytes":         nodeConfig.MaxSizeBytes,
					"max_disk_usage_percent": nodeConfig.MaxDiskUsagePercent,
				},
			},
			"storers": []string{"simplefs"},
		},
		"API": map[string]any{
			"souin": map[string]any{
				"enable":   true,
				"basepath": "/__goveto/cache/" + siteID,
			},
		},
	}
	return map[string]any{"handler": "cache", "Configuration": configuration}
}

func staleIfErrorTTL(policy cachepolicy.CachePolicy) int {
	if !policy.Stale.Enabled {
		return 0
	}
	return policy.Stale.IfErrorSeconds
}

func stringMapValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
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
