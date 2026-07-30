package edgeagent

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/standard"

	_ "goveto-edge/caddy/compression"
	_ "goveto-edge/caddy/origingovernance"
	cachefs "goveto-edge/caddy/simplefs"
	_ "goveto-edge/caddy/splitmatch"
	"goveto-edge/internal/edgeprotocol"
	cachepolicy "goveto-edge/internal/policy"
)

type ConfigManager struct {
	mu            sync.Mutex
	sites         map[string]SiteConfig
	path          string
	defaultListen string
	nodeConfig    NodeConfig
	geoIPPath     string
}

var ErrGeoIPUnavailable = errors.New("managed GeoIP database is not installed")

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
	if path != "" {
		manager.geoIPPath = filepath.Join(filepath.Dir(path), "geoip", "GeoLite2-City.mmdb")
	}
	return manager
}

func (m *ConfigManager) SetNodeConfig(config NodeConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	config.MaxDiskUsagePercent = normalizeCacheDiskPercent(config.MaxDiskUsagePercent)

	encoded, err := renderManagedCaddyConfig(m.sites, m.defaultListen, m.geoIPPath, config)
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
	m.mu.Lock()
	m.sites = sites
	m.mu.Unlock()
	return m.load(sites)
}

func (m *ConfigManager) load(sites map[string]SiteConfig) error {
	encoded, err := renderManagedCaddyConfig(sites, m.defaultListen, m.geoIPPath, m.nodeConfig)
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

	previousEncoded, err := renderManagedCaddyConfig(m.sites, m.defaultListen, m.geoIPPath, m.nodeConfig)
	if err != nil {
		return fmt.Errorf("render previous site config: %w", err)
	}

	candidate := cloneSites(m.sites)
	candidate[config.SiteID] = config

	encoded, err := renderManagedCaddyConfig(candidate, m.defaultListen, m.geoIPPath, m.nodeConfig)
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
		if site.Disabled {
			continue
		}
		versions[id] = site.Version
	}
	return versions
}

func (m *ConfigManager) Stop() error { m.mu.Lock(); defer m.mu.Unlock(); return caddy.Stop() }

// Reload reapplies all persisted sites after a managed node asset changes.
func (m *ConfigManager) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	encoded, err := renderManagedCaddyConfig(m.sites, m.defaultListen, m.geoIPPath, m.nodeConfig)
	if err != nil {
		return err
	}
	return caddy.Load(encoded, true)
}

func (m *ConfigManager) Purge(_ context.Context, purge edgeprotocol.PurgeRequest) error {
	_, err := m.PurgeDetailed(purge)
	return err
}

func (m *ConfigManager) PurgeDetailed(purge edgeprotocol.PurgeRequest) (edgeprotocol.PurgeResult, error) {
	result := edgeprotocol.PurgeResult{Type: purge.Type}
	if err := purge.Validate(); err != nil {
		return result, fmt.Errorf("invalid purge request: %w", err)
	}
	m.mu.Lock()
	site, ok := m.sites[purge.SiteID]
	cacheDirectory := m.nodeConfig.CacheDirectory
	m.mu.Unlock()

	if !ok || site.Disabled {
		return result, errors.New("site config is not active")
	}
	cachePolicy, configured, err := decodeCachePolicy(site.Cache)
	if err != nil {
		return result, fmt.Errorf("invalid site cache policy: %w", err)
	}
	if !configured || !cachePolicy.Enabled {
		return result, errors.New("site cache is not enabled")
	}
	if len(site.Domains) == 0 {
		return result, errors.New("site has no domain")
	}
	result.Objects, err = cachefs.Purge(
		filepath.Join(cacheDirectory, site.SiteID),
		purge.Type,
		site.Domains,
		purge.Values,
	)
	return result, err
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
	return renderManagedCaddyConfig(sites, defaultListen, "", nodeConfigs...)
}

func renderManagedCaddyConfig(sites map[string]SiteConfig, defaultListen, geoIPPath string, nodeConfigs ...NodeConfig) ([]byte, error) {
	nodeConfig := NodeConfig{
		CacheDirectory:      "/opt/goveto-edge/cache",
		AutoMaxSize:         true,
		MaxDiskUsagePercent: 80,
	}
	if len(nodeConfigs) > 0 {
		nodeConfig = nodeConfigs[0]
		nodeConfig.MaxDiskUsagePercent = normalizeCacheDiskPercent(nodeConfig.MaxDiskUsagePercent)
	}

	ids := make([]string, 0, len(sites))
	for id := range sites {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	routes := make([]any, 0, len(ids)*2)
	errorRoutes := make([]any, 0)
	loggerNames := make(map[string][]string, len(ids))
	customLogs := map[string]any{
		"default": map[string]any{
			"writer":  map[string]any{"output": "goveto_buffer"},
			"exclude": []string{"http.log.access"},
		},
	}

	listeners, protocols := map[string]struct{}{}, map[string]struct{}{"h1": {}}
	listeners[defaultListen] = struct{}{}
	policies, certificates := make([]any, 0), make([]any, 0)
	var geoIPValidationOnce sync.Once
	var geoIPValidationErr error
	validateGeoIP := func() error {
		geoIPValidationOnce.Do(func() {
			if geoIPPath == "" {
				geoIPValidationErr = ErrGeoIPUnavailable
				return
			}
			if _, err := validateCityDatabase(geoIPPath); err != nil {
				geoIPValidationErr = errors.Join(ErrGeoIPUnavailable, fmt.Errorf("validate managed GeoIP database: %w", err))
			}
		})
		return geoIPValidationErr
	}

	for _, id := range ids {
		site := sites[id]
		if site.Disabled {
			continue
		}
		loggerName := "site_" + id
		customLogs[loggerName] = map[string]any{
			"writer": map[string]any{
				"output": "goveto_buffer", "site_id": id, "config_version": site.Version,
			},
			"include": []string{"http.log.access." + loggerName},
		}
		for _, domain := range site.Domains {
			loggerNames[strings.ToLower(domain)] = []string{loggerName}
		}

		listener := site.Listener
		for _, challenge := range site.ACMEChallenges {
			if challenge.Domain == "" || challenge.Token == "" || challenge.KeyAuth == "" {
				return nil, fmt.Errorf("site %s has invalid ACME HTTP-01 challenge", id)
			}
			routes = append(routes, map[string]any{
				"@id": "site_" + id + "_acme_" + challenge.Token,
				"match": []any{map[string]any{
					"host": []string{challenge.Domain},
					"path": []string{"/.well-known/acme-challenge/" + challenge.Token},
				}},
				"handle": []any{map[string]any{
					"handler": "static_response", "status_code": 200, "body": challenge.KeyAuth,
					"headers": map[string][]string{"Content-Type": {"text/plain"}},
				}},
				"terminal": true,
			})
		}
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
		deliveryPolicy, deliveryConfigured, err := decodeDeliveryPolicy(site.Delivery)
		if err != nil {
			return nil, fmt.Errorf("site %s delivery policy: %w", id, err)
		}
		if deliveryConfigured {
			if err = validateDeliveryPoolProtocols(deliveryPolicy); err != nil {
				return nil, fmt.Errorf("site %s delivery policy: %w", id, err)
			}
			routes = append(routes, deliveryPreludeRoutes(site, deliveryPolicy)...)
			errorRoutes = append(errorRoutes, deliveryErrorRoutes(site, deliveryPolicy)...)
		}
		errorRoutes = append(errorRoutes, map[string]any{
			"@id":   "site_" + id + "_error_fallback",
			"match": []any{map[string]any{"host": site.Domains}},
			"handle": []any{
				handlerErrorLogAppender(),
				map[string]any{"handler": "static_response", "status_code": "{http.error.status_code}"},
			},
			"terminal": true,
		})

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

		handlers := []any{
			logAppender("upstream_address", "{goveto.origin.address}"),
			logAppender("upstream_status", "{http.reverse_proxy.status_code}"),
		}
		wafPolicy, wafConfigured, err := decodeWAFPolicy(site.WAF)
		if err != nil {
			return nil, fmt.Errorf("site %s WAF policy: %w", id, err)
		}
		rateLimitPolicy, rateLimitConfigured, err := decodeRateLimitPolicy(site.RateLimit)
		if err != nil {
			return nil, fmt.Errorf("site %s rate-limit policy: %w", id, err)
		}
		accessPolicy, accessConfigured, err := decodeAccessPolicy(site.Access, geoIPPath, validateGeoIP)
		if err != nil {
			return nil, fmt.Errorf("site %s access policy: %w", id, err)
		}
		if (wafConfigured && wafPolicy.Enabled) || (rateLimitConfigured && rateLimitPolicy.Enabled) || (accessConfigured && accessPolicy.Enabled) {
			securityHandler := map[string]any{
				"handler":    "goveto_waf",
				"site_id":    id,
				"waf":        wafPolicy,
				"access":     accessPolicy,
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
		if deliveryConfigured && len(deliveryPolicy.ResponseHeaders) > 0 {
			handlers = append(handlers, deliveryResponseHandler(deliveryPolicy))
		}

		originPolicy := edgeprotocol.NormalizeOriginPolicy(site.OriginPolicy)
		upstreams := make([]any, 0, len(site.Origins))
		originBackends := make([]any, 0, len(site.Origins))
		for _, origin := range site.Origins {
			dial := originDialAddress(origin.Address, originPolicy.Transport.IPVersion)
			upstream := map[string]any{"dial": dial}
			if originPolicy.PassiveHealth.UnhealthyRequestCount > 0 {
				upstream["max_requests"] = originPolicy.PassiveHealth.UnhealthyRequestCount
			}
			upstreams = append(upstreams, upstream)
			weight := origin.Weight
			if weight < 1 {
				weight = 1
			}
			originBackends = append(originBackends, map[string]any{
				"dial": dial, "host_header": origin.HostHeader, "weight": weight, "priority": origin.Priority,
			})
		}

		policy := site.Scheduler
		if policy == "" {
			policy = "round_robin"
		}
		if !supportedOriginScheduler(policy) {
			return nil, fmt.Errorf("unsupported Caddy scheduler %q", policy)
		}
		reverseProxy := map[string]any{
			"handler":   "reverse_proxy",
			"upstreams": upstreams,
			"load_balancing": map[string]any{
				"selection_policy": map[string]any{
					"policy": "goveto_origin", "site_id": id, "scheduler": policy, "backends": originBackends,
				},
				"retries":      originPolicy.Retry.Retries,
				"try_duration": durationMS(originPolicy.Retry.TryDurationMS),
				"try_interval": durationMS(originPolicy.Retry.TryIntervalMS),
				"retry_match": []any{map[string]any{
					"method":     []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodTrace},
					"expression": "{http.reverse_proxy.is_transport_error} == true || {http.reverse_proxy.status_code} in [502, 503, 504]",
				}},
			},
		}
		healthChecks := map[string]any{}
		if originPolicy.PassiveHealth.Enabled {
			healthChecks["passive"] = map[string]any{
				"fail_duration": durationMS(originPolicy.PassiveHealth.FailDurationMS),
				"max_fails":     originPolicy.PassiveHealth.MaxFails,
			}
		}
		if len(healthChecks) > 0 {
			reverseProxy["health_checks"] = healthChecks
		}

		transport := map[string]any{
			"protocol":                "goveto_http",
			"site_id":                 id,
			"dial_timeout":            durationMS(originPolicy.Transport.DialTimeoutMS),
			"response_header_timeout": durationMS(originPolicy.Transport.ResponseHeaderTimeoutMS),
			"read_timeout":            durationMS(originPolicy.Transport.ReadTimeoutMS),
			"write_timeout":           durationMS(originPolicy.Transport.WriteTimeoutMS),
			"keep_alive": map[string]any{
				"max_idle_conns_per_host": originPolicy.Transport.KeepAliveMaxIdleConnsPerHost,
				"idle_timeout":            durationMS(originPolicy.Transport.KeepAliveIdleTimeoutMS),
			},
		}
		if originPolicy.Transport.MaxConnsPerHost > 0 {
			transport["max_conns_per_host"] = originPolicy.Transport.MaxConnsPerHost
		}
		if strings.EqualFold(site.Origins[0].Protocol, "https") {
			tlsConfig := map[string]any{
				"handshake_timeout":    durationMS(originPolicy.Transport.TLSHandshakeTimeoutMS),
				"server_name":          originPolicy.Transport.TLSServerName,
				"insecure_skip_verify": originPolicy.Transport.TLSInsecureSkipVerify,
			}
			trustedCertificates, err := inlineCACertificates(originPolicy.Transport.TLSRootCAPEM)
			if err != nil {
				return nil, fmt.Errorf("site %s origin private CA: %w", id, err)
			}
			if len(trustedCertificates) > 0 {
				tlsConfig["ca"] = map[string]any{"provider": "inline", "trusted_ca_certs": trustedCertificates}
			}
			transport["tls"] = tlsConfig
			transport["client_certificate_pem"] = originPolicy.Transport.TLSClientCertificatePEM
			transport["client_private_key_pem"] = originPolicy.Transport.TLSClientPrivateKeyPEM
		}
		if deliveryConfigured && deliveryPolicy.Protocols.GRPC {
			transport["versions"] = []string{"1.1", "2", "h2c"}
		}
		reverseProxy["transport"] = transport

		requestHeaders := cloneHeaders(originPolicy.Headers)
		requestHeaders["Host"] = []string{"{goveto.origin.host}"}
		reverseProxy["headers"] = map[string]any{"request": map[string]any{"set": requestHeaders}}
		if deliveryConfigured {
			applyDeliveryProxy(reverseProxy, deliveryPolicy)
		}

		originMetrics := map[string]any{
			"handler": "goveto_origin_metrics", "site_id": id, "timeout": durationMS(originPolicy.TimeoutMS),
		}
		if deliveryConfigured && deliveryPolicy.CORS.Enabled {
			routes = append(routes, deliveryCORSRoute(site, deliveryPolicy))
		}
		if deliveryConfigured && (len(deliveryPolicy.Splits) > 0 || len(deliveryPolicy.OriginPools) > 0) {
			poolHandlers := append(append([]any{}, handlers...), originMetrics)
			poolRoutes, routeErr := deliveryPoolRoutes(site, deliveryPolicy, poolHandlers, reverseProxy, originPolicy)
			if routeErr != nil {
				return nil, fmt.Errorf("site %s delivery pools: %w", id, routeErr)
			}
			routes = append(routes, poolRoutes...)
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
				"handler":                    "goveto_cache_headers",
				"site_id":                    id,
				"x_cache":                    cachePolicy.ResponseHeaders.XCache,
				"age":                        cachePolicy.ResponseHeaders.Age,
				"stale_while_revalidate_ttl": staleWhileRevalidateTTL(cachePolicy),
				"background_revalidate":      cachePolicy.Stale.Enabled && cachePolicy.Stale.WhileRevalidateSeconds > 0,
				"coalesce":                   cachePolicy.RequestCoalescing,
				"coalesce_headers":           cacheKeyHeaders(cachePolicy),
			})
			cachedHandlers = append(cachedHandlers,
				cacheHandler,
				map[string]any{
					"handler":            "goveto_cache_headers",
					"age":                true,
					"default_ttl":        cachePolicy.TTL.DefaultSeconds,
					"status_ttl":         cachePolicy.TTL.Status,
					"stale_if_error_ttl": staleIfErrorTTL(cachePolicy),
					"validate_upstream":  true,
				},
				originMetrics, reverseProxy,
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
							"conditions":           cachePolicy.Conditions,
							"cache_range_requests": cachePolicy.CacheRangeRequests,
						},
					},
				},
				"handle":   cachedHandlers,
				"terminal": true,
			})
		}

		handlers = append(handlers, originMetrics, reverseProxy)
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
			"logs": map[string]any{
				"logger_names": loggerNames, "skip_unmapped_hosts": true,
			},
			"errors": map[string]any{"routes": errorRoutes},
		},
	}
	config := map[string]any{
		"admin":   map[string]any{"disabled": true},
		"logging": map[string]any{"logs": customLogs},
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

func decodeAccessPolicy(raw map[string]any, geoIPPath string, validateGeoIP func() error) (cachepolicy.AccessPolicy, bool, error) {
	if raw == nil {
		return cachepolicy.AccessPolicy{}, false, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return cachepolicy.AccessPolicy{}, false, err
	}
	policy := cachepolicy.DefaultAccessPolicy()
	if err = json.Unmarshal(data, &policy); err != nil {
		return policy, false, err
	}
	policy.GeoIPDatabase = ""
	if err = policy.NormalizeAndValidate(); err != nil {
		return policy, false, err
	}
	needsGeoIP := len(policy.AllowedCountries)+len(policy.BlockedCountries)+len(policy.AllowedRegions)+len(policy.BlockedRegions) > 0
	if needsGeoIP {
		if err = validateGeoIP(); err != nil {
			return policy, false, err
		}
		policy.GeoIPDatabase = geoIPPath
	}
	return policy, true, nil
}

func souinHandler(siteID string, policy cachepolicy.CachePolicy, nodeConfig NodeConfig) map[string]any {
	stale := "0s"
	if policy.Stale.Enabled {
		staleSeconds := max(policy.Stale.IfErrorSeconds, policy.Stale.WhileRevalidateSeconds)
		stale = strconv.Itoa(staleSeconds) + "s"
	}
	keyHeaders := cacheKeyHeaders(policy)

	verbs := []string{http.MethodGet, http.MethodHead}
	if policy.AllowPurgeMethod {
		verbs = append(verbs, "PURGE")
	}

	configuration := map[string]any{
		"DefaultCache": map[string]any{
			"allowed_http_verbs":       verbs,
			"cache_name":               "Goveto-" + siteID,
			"key":                      map[string]any{"headers": keyHeaders},
			"mode":                     "strict",
			"disable_coalescing":       true,
			"max_cacheable_body_bytes": policy.MaxBodyBytes,
			"ttl":                      strconv.Itoa(policy.TTL.DefaultSeconds) + "s",
			"stale":                    stale,
			"default_cache_control":    "public, max-age=" + strconv.Itoa(policy.TTL.DefaultSeconds),
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

func staleWhileRevalidateTTL(policy cachepolicy.CachePolicy) int {
	if !policy.Stale.Enabled {
		return 0
	}
	return policy.Stale.WhileRevalidateSeconds
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func cacheKeyHeaders(policy cachepolicy.CachePolicy) []string {
	headers := append([]string(nil), policy.VaryHeaders...)
	if policy.CacheRangeRequests {
		for _, header := range []string{"Range", "If-Range"} {
			if !containsFold(headers, header) {
				headers = append(headers, header)
			}
		}
	}
	return headers
}

func durationMS(value int) time.Duration {
	return time.Duration(value) * time.Millisecond
}

func originDialAddress(address, policy string) string {
	switch policy {
	case "ipv4":
		return "tcp4/" + address
	case "ipv6":
		return "tcp6/" + address
	default:
		return address
	}
}

func supportedOriginScheduler(policy string) bool {
	switch policy {
	case "round_robin", "weighted_round_robin", "least_conn", "random", "first", "ip_hash":
		return true
	default:
		return false
	}
}

func cloneHeaders(source map[string][]string) map[string][]string {
	target := make(map[string][]string, len(source)+1)
	for name, values := range source {
		target[name] = append([]string(nil), values...)
	}
	return target
}

func inlineCACertificates(pemValues []string) ([]string, error) {
	var certificates []string
	for index, value := range pemValues {
		remaining := []byte(value)
		found := false
		for len(strings.TrimSpace(string(remaining))) > 0 {
			block, rest := pem.Decode(remaining)
			if block == nil {
				return nil, fmt.Errorf("entry %d contains invalid PEM", index)
			}
			remaining = rest
			if block.Type != "CERTIFICATE" {
				continue
			}
			if _, err := x509.ParseCertificate(block.Bytes); err != nil {
				return nil, fmt.Errorf("entry %d: %w", index, err)
			}
			certificates = append(certificates, base64.StdEncoding.EncodeToString(block.Bytes))
			found = true
		}
		if !found {
			return nil, fmt.Errorf("entry %d contains no certificate", index)
		}
	}
	return certificates, nil
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
