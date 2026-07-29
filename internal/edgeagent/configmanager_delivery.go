package edgeagent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"goveto-edge/internal/edgeprotocol"
	deliverypolicy "goveto-edge/internal/policy"
)

func decodeDeliveryPolicy(raw map[string]any) (deliverypolicy.DeliveryPolicy, bool, error) {
	policy := deliverypolicy.DefaultDeliveryPolicy()
	if raw == nil || len(raw) == 0 {
		return policy, false, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return policy, false, err
	}
	if err = json.Unmarshal(data, &policy); err != nil {
		return policy, false, err
	}
	if err = policy.NormalizeAndValidate(); err != nil {
		return policy, false, err
	}
	return policy, true, nil
}

func deliveryPreludeRoutes(site SiteConfig, policy deliverypolicy.DeliveryPolicy) []any {
	routes := make([]any, 0, len(policy.Redirects)+len(policy.Rewrites)+3)
	if policy.Maintenance.Enabled {
		routes = append(routes, map[string]any{
			"@id":   "site_" + site.SiteID + "_maintenance",
			"match": []any{map[string]any{"host": site.Domains}},
			"handle": []any{map[string]any{
				"handler": "static_response", "status_code": policy.Maintenance.Status,
				"body":    policy.Maintenance.Body,
				"headers": map[string][]string{"Content-Type": {policy.Maintenance.ContentType}, "Retry-After": {"300"}},
			}},
			"terminal": true,
		})
	}
	for index, rule := range policy.Redirects {
		routes = append(routes, map[string]any{
			"@id":   "site_" + site.SiteID + "_redirect_" + strconv.Itoa(index),
			"match": []any{map[string]any{"host": site.Domains, "path": []string{rule.Path}}},
			"handle": []any{map[string]any{
				"handler": "static_response", "status_code": rule.Status,
				"headers": map[string][]string{"Location": {rule.Location}},
			}},
			"terminal": true,
		})
	}
	if policy.CORS.Enabled {
		headers := corsHeaders(policy.CORS)
		matcher := map[string]any{
			"host": site.Domains, "method": []string{http.MethodOptions},
			"header": map[string][]string{
				"Origin":                        corsOriginMatcher(policy.CORS.AllowOrigins),
				"Access-Control-Request-Method": {"*"},
			},
		}
		routes = append(routes, map[string]any{
			"@id":      "site_" + site.SiteID + "_cors_preflight",
			"match":    []any{matcher},
			"handle":   []any{map[string]any{"handler": "static_response", "status_code": http.StatusNoContent, "headers": headers}},
			"terminal": true,
		})
	}
	if !policy.Protocols.GRPC {
		routes = append(routes, protocolRejectionRoute(site, "grpc", map[string]any{
			"header": map[string][]string{"Content-Type": {"application/grpc*"}},
		}))
	}
	if !policy.Protocols.WebSocket || !policy.Protocols.HTTPUpgrade {
		upgradeMatcher := map[string]any{"header": map[string][]string{"Upgrade": {"*"}}}
		switch {
		case policy.Protocols.WebSocket:
			upgradeMatcher["not"] = []any{websocketMatcher()}
		case policy.Protocols.HTTPUpgrade:
			upgradeMatcher = websocketMatcher()
		}
		routes = append(routes, protocolRejectionRoute(site, "upgrade", upgradeMatcher))
	}
	for index, rule := range policy.Rewrites {
		routes = append(routes, map[string]any{
			"@id":    "site_" + site.SiteID + "_rewrite_" + strconv.Itoa(index),
			"match":  []any{map[string]any{"host": site.Domains, "path": []string{rule.Path}}},
			"handle": []any{map[string]any{"handler": "rewrite", "uri": rule.Replacement}},
		})
	}
	return routes
}

func protocolRejectionRoute(site SiteConfig, suffix string, requestMatcher map[string]any) map[string]any {
	requestMatcher["host"] = site.Domains
	return map[string]any{
		"@id":   "site_" + site.SiteID + "_reject_" + suffix,
		"match": []any{requestMatcher},
		"handle": []any{map[string]any{
			"handler": "static_response", "status_code": http.StatusUpgradeRequired,
			"body":    "protocol upgrade is disabled\n",
			"headers": map[string][]string{"Content-Type": {"text/plain; charset=utf-8"}},
		}},
		"terminal": true,
	}
}

func websocketMatcher() map[string]any {
	return map[string]any{
		"header_regexp": map[string]any{"Upgrade": map[string]any{"pattern": `(?i)^websocket$`}},
	}
}

func deliveryResponseHandler(policy deliverypolicy.DeliveryPolicy) map[string]any {
	config := headerOperations(policy.ResponseHeaders)
	config["deferred"] = true
	return map[string]any{"handler": "headers", "response": config}
}

func applyDeliveryProxy(proxy map[string]any, policy deliverypolicy.DeliveryPolicy) {
	headers, _ := proxy["headers"].(map[string]any)
	request, _ := headers["request"].(map[string]any)
	mergeHeaderOperations(request, headerOperations(policy.RequestHeaders))
	headers["request"] = request
	proxy["headers"] = headers
	if policy.OriginPrefix != "" {
		proxy["rewrite"] = map[string]any{"uri": policy.OriginPrefix + "{http.request.uri.path}?{http.request.uri.query}"}
	}
	if len(policy.ErrorPages) > 0 {
		handlers := make([]any, 0, len(policy.ErrorPages))
		for _, page := range policy.ErrorPages {
			handlers = append(handlers, map[string]any{
				"match": map[string]any{"status_code": page.Statuses},
				"routes": []any{map[string]any{"handle": []any{map[string]any{
					"handler": "static_response", "status_code": "{http.reverse_proxy.status_code}", "body": page.Body,
					"headers": map[string][]string{"Content-Type": {page.ContentType}},
				}}}},
			})
		}
		proxy["handle_response"] = handlers
	}
}

func headerOperations(rules []deliverypolicy.HeaderRule) map[string]any {
	result := map[string]any{}
	set, add := map[string][]string{}, map[string][]string{}
	deletes := make([]string, 0)
	for _, rule := range rules {
		switch rule.Operation {
		case "ADD":
			add[rule.Name] = append(add[rule.Name], rule.Value)
		case "DELETE":
			deletes = append(deletes, rule.Name)
		default:
			set[rule.Name] = []string{rule.Value}
		}
	}
	if len(set) > 0 {
		result["set"] = set
	}
	if len(add) > 0 {
		result["add"] = add
	}
	if len(deletes) > 0 {
		sort.Strings(deletes)
		result["delete"] = deletes
	}
	return result
}

func mergeHeaderOperations(target, additions map[string]any) {
	for operation, raw := range additions {
		if operation == "delete" {
			current, _ := target[operation].([]string)
			target[operation] = append(current, raw.([]string)...)
			continue
		}
		current, _ := target[operation].(map[string][]string)
		if current == nil {
			current = map[string][]string{}
		}
		for name, values := range raw.(map[string][]string) {
			current[name] = values
		}
		target[operation] = current
	}
}

func corsHeaders(config deliverypolicy.CORSConfig) map[string][]string {
	origin := strings.Join(config.AllowOrigins, " ")
	if len(config.AllowOrigins) > 1 {
		origin = "{http.request.header.Origin}"
	}
	result := map[string][]string{
		"Access-Control-Allow-Origin":  {origin},
		"Access-Control-Allow-Methods": {strings.Join(config.AllowMethods, ", ")},
		"Vary":                         {"Origin"},
	}
	if len(config.AllowHeaders) > 0 {
		result["Access-Control-Allow-Headers"] = []string{strings.Join(config.AllowHeaders, ", ")}
	}
	if len(config.ExposeHeaders) > 0 {
		result["Access-Control-Expose-Headers"] = []string{strings.Join(config.ExposeHeaders, ", ")}
	}
	if config.AllowCredentials {
		result["Access-Control-Allow-Credentials"] = []string{"true"}
	}
	if config.MaxAgeSeconds > 0 {
		result["Access-Control-Max-Age"] = []string{strconv.Itoa(config.MaxAgeSeconds)}
	}
	return result
}

func deliveryCORSRoute(site SiteConfig, policy deliverypolicy.DeliveryPolicy) map[string]any {
	matcher := map[string]any{
		"host":   site.Domains,
		"header": map[string][]string{"Origin": corsOriginMatcher(policy.CORS.AllowOrigins)},
	}
	return map[string]any{
		"@id":   "site_" + site.SiteID + "_cors",
		"match": []any{matcher},
		"handle": []any{map[string]any{
			"handler": "headers", "response": map[string]any{"set": corsHeaders(policy.CORS), "deferred": true},
		}},
	}
}

func deliveryPoolRoutes(site SiteConfig, policy deliverypolicy.DeliveryPolicy, handlers []any, baseProxy map[string]any, originPolicy edgeprotocol.OriginPolicyConfig) ([]any, error) {
	pools := make(map[string]deliverypolicy.PathOriginPool, len(policy.OriginPools))
	for _, pool := range policy.OriginPools {
		pools[pool.Name] = pool
	}
	routes := make([]any, 0, len(policy.Splits)+len(policy.OriginPools))
	for index, split := range policy.Splits {
		pool := pools[split.Pool]
		proxy, err := deliveryProxy(baseProxy, pool, site.SiteID, originPolicy)
		if err != nil {
			return nil, err
		}
		matched := map[string]any{"host": site.Domains, "goveto_split": map[string]any{
			"header_name": split.HeaderName, "cookie_name": split.CookieName, "value": split.Value,
			"percentage": split.Percentage, "salt": site.SiteID + ":" + split.Name,
		}, "path": pool.Paths}
		routes = append(routes, map[string]any{
			"@id": "site_" + site.SiteID + "_split_" + strconv.Itoa(index), "match": []any{matched},
			"handle": append(append([]any{}, handlers...), proxy), "terminal": true,
		})
	}
	for index, pool := range policy.OriginPools {
		proxy, err := deliveryProxy(baseProxy, pool, site.SiteID, originPolicy)
		if err != nil {
			return nil, err
		}
		routes = append(routes, map[string]any{
			"@id":    "site_" + site.SiteID + "_pool_" + strconv.Itoa(index),
			"match":  []any{map[string]any{"host": site.Domains, "path": pool.Paths}},
			"handle": append(append([]any{}, handlers...), proxy), "terminal": true,
		})
	}
	return routes, nil
}

func deliveryProxy(base map[string]any, pool deliverypolicy.PathOriginPool, siteID string, originPolicy edgeprotocol.OriginPolicyConfig) (map[string]any, error) {
	proxy := cloneDeliveryMap(base)
	upstreams := make([]any, 0, len(pool.Origins))
	backends := make([]any, 0, len(pool.Origins))
	for _, origin := range pool.Origins {
		dial := originDialAddress(origin.Address, originPolicy.Transport.IPVersion)
		upstreams = append(upstreams, map[string]any{"dial": dial})
		backends = append(backends, map[string]any{"dial": dial, "host_header": origin.HostHeader, "weight": origin.Weight})
	}
	proxy["upstreams"] = upstreams
	loadBalancing := proxy["load_balancing"].(map[string]any)
	loadBalancing["selection_policy"] = map[string]any{"policy": "goveto_origin", "site_id": siteID, "scheduler": pool.Scheduler, "backends": backends}
	transport := proxy["transport"].(map[string]any)
	if strings.EqualFold(pool.Origins[0].Protocol, "https") {
		if _, configured := transport["tls"]; !configured {
			tlsConfig := map[string]any{
				"handshake_timeout":    durationMS(originPolicy.Transport.TLSHandshakeTimeoutMS),
				"server_name":          originPolicy.Transport.TLSServerName,
				"insecure_skip_verify": originPolicy.Transport.TLSInsecureSkipVerify,
			}
			trustedCertificates, err := inlineCACertificates(originPolicy.Transport.TLSRootCAPEM)
			if err != nil {
				return nil, fmt.Errorf("origin pool %q private CA: %w", pool.Name, err)
			}
			if len(trustedCertificates) > 0 {
				tlsConfig["ca"] = map[string]any{"provider": "inline", "trusted_ca_certs": trustedCertificates}
			}
			transport["tls"] = tlsConfig
		}
		transport["client_certificate_pem"] = originPolicy.Transport.TLSClientCertificatePEM
		transport["client_private_key_pem"] = originPolicy.Transport.TLSClientPrivateKeyPEM
	} else {
		delete(transport, "tls")
		delete(transport, "client_certificate_pem")
		delete(transport, "client_private_key_pem")
	}
	return proxy, nil
}

func deliveryErrorRoutes(site SiteConfig, policy deliverypolicy.DeliveryPolicy) []any {
	routes := make([]any, 0, len(policy.ErrorPages))
	for index, page := range policy.ErrorPages {
		statuses := make([]string, len(page.Statuses))
		for i, status := range page.Statuses {
			statuses[i] = strconv.Itoa(status)
		}
		routes = append(routes, map[string]any{
			"@id": "site_" + site.SiteID + "_error_" + strconv.Itoa(index),
			"match": []any{map[string]any{
				"host":       site.Domains,
				"expression": "{http.error.status_code} in [" + strings.Join(statuses, ",") + "]",
			}},
			"handle": []any{
				handlerErrorLogAppender(),
				map[string]any{
					"handler": "static_response", "status_code": "{http.error.status_code}", "body": page.Body,
					"headers": map[string][]string{"Content-Type": {page.ContentType}},
				},
			}, "terminal": true,
		})
	}
	return routes
}

func logAppender(key, value string) map[string]any {
	return map[string]any{"handler": "log_append", "key": key, "value": value}
}

func handlerErrorLogAppender() map[string]any {
	return logAppender("handler_error", "{http.error.message}")
}

func cloneDeliveryMap(source map[string]any) map[string]any {
	encoded, _ := json.Marshal(source)
	var target map[string]any
	_ = json.Unmarshal(encoded, &target)
	return target
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func corsOriginMatcher(origins []string) []string {
	if containsString(origins, "*") {
		return []string{"*"}
	}
	return origins
}

func validateDeliveryPoolProtocols(policy deliverypolicy.DeliveryPolicy) error {
	for _, pool := range policy.OriginPools {
		protocol := pool.Origins[0].Protocol
		for _, origin := range pool.Origins[1:] {
			if origin.Protocol != protocol {
				return fmt.Errorf("origin pool %q cannot mix HTTP and HTTPS origins", pool.Name)
			}
		}
	}
	return nil
}
