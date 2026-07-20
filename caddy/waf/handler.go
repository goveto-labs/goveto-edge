package waf

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"goveto-edge/internal/policy"
)

type Handler struct {
	SiteID    string                 `json:"site_id"`
	WAF       policy.WAFPolicy       `json:"waf"`
	RateLimit policy.RateLimitPolicy `json:"rate_limit"`

	groups      []compiledGroup
	rateRules   []compiledRateRule
	inspectBody bool
}

type compiledGroup struct {
	policy.WAFRuleGroup
	rules []compiledRule
}

type compiledRateRule struct {
	policy.RateLimitRule
	conditions compiledConditions
	limiter    *counterStore
}

type compiledConditions struct {
	operator string
	groups   []compiledConditionGroup
}

type compiledConditionGroup struct {
	operator string
	rules    []compiledRule
}

type compiledRule struct {
	policy.WAFRequestRule
	regex *regexp.Regexp
	cidrs []netip.Prefix
}

type requestData struct {
	request *http.Request
	body    string
	ip      string
}

func init() { caddy.RegisterModule(Handler{}) }

func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.goveto_waf", New: func() caddy.Module { return new(Handler) }}
}

func (h *Handler) Provision(_ caddy.Context) error {
	if h.SiteID == "" {
		return fmt.Errorf("site_id is required")
	}
	if err := h.WAF.NormalizeAndValidate(); err != nil {
		return fmt.Errorf("invalid WAF policy: %w", err)
	}
	if err := h.RateLimit.NormalizeAndValidate(); err != nil {
		return fmt.Errorf("invalid rate-limit policy: %w", err)
	}

	h.groups = make([]compiledGroup, 0, len(h.WAF.Groups))
	for _, group := range h.WAF.Groups {
		if !group.Enabled {
			continue
		}
		rules, inspectBody, err := compileRules(group.Rules)
		if err != nil {
			return err
		}
		h.inspectBody = h.inspectBody || inspectBody
		h.groups = append(h.groups, compiledGroup{WAFRuleGroup: group, rules: rules})
	}
	if h.WAF.Enabled && len(h.WAF.Presets) > 0 {
		h.inspectBody = true
	}

	h.rateRules = make([]compiledRateRule, 0, len(h.RateLimit.Rules))
	for _, rule := range h.RateLimit.Rules {
		if !rule.Enabled {
			continue
		}
		conditions, inspectBody, err := compileConditions(rule.Conditions)
		if err != nil {
			return err
		}
		h.inspectBody = h.inspectBody || inspectBody
		h.rateRules = append(h.rateRules, compiledRateRule{
			RateLimitRule: rule,
			conditions:    conditions,
			limiter:       limiterFor(h.SiteID, rule.ID),
		})
	}
	return nil
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	data := requestData{request: r, ip: clientIP(r)}
	if h.inspectBody && r.Body != nil && h.WAF.MaxBodyBytes > 0 {
		body, err := io.ReadAll(io.LimitReader(r.Body, h.WAF.MaxBodyBytes))
		if err != nil {
			return err
		}
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		data.body = string(body)
	}

	if h.WAF.Enabled {
		if id, action, status := h.matchWAF(data); id != "" {
			if action == "ALLOW" {
				return next.ServeHTTP(w, r)
			}
			if action == "MONITOR" || h.WAF.Mode == policy.WAFModeMonitor {
				w.Header().Set("X-Goveto-WAF", "MONITOR")
				w.Header().Set("X-Goveto-WAF-Rule", id)
			} else {
				w.Header().Set("X-Goveto-WAF", "BLOCK")
				w.Header().Set("X-Goveto-WAF-Rule", id)
				http.Error(w, http.StatusText(status), status)
				return nil
			}
		}
	}

	if h.RateLimit.Enabled {
		now := time.Now()
		for index := range h.rateRules {
			rule := &h.rateRules[index]
			if !rule.conditions.match(data) {
				continue
			}
			key := rateKey(rule.RateLimitRule, data)
			allowed, retryAfter := rule.limiter.allow(key, now, rule.RateLimitRule)
			if allowed {
				continue
			}
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Seconds()+0.5))))
			w.Header().Set("X-Goveto-WAF", "RATE_LIMIT")
			w.Header().Set("X-Goveto-WAF-Rule", rule.ID)
			http.Error(w, http.StatusText(rule.StatusCode), rule.StatusCode)
			return nil
		}
	}

	return next.ServeHTTP(w, r)
}

func (h Handler) matchWAF(data requestData) (string, string, int) {
	for _, group := range h.groups {
		if group.Action != "ALLOW" || !group.match(data) {
			continue
		}
		return group.ID, group.Action, group.StatusCode
	}
	for _, preset := range h.WAF.Presets {
		if matchPreset(preset, data) {
			return "preset:" + preset, "BLOCK", h.WAF.BlockStatus
		}
	}
	for _, group := range h.groups {
		if group.Action != "ALLOW" && group.match(data) {
			return group.ID, group.Action, group.StatusCode
		}
	}
	return "", "", 0
}

func (g compiledGroup) match(data requestData) bool {
	matches := make([]bool, len(g.rules))
	for index := range g.rules {
		matches[index] = g.rules[index].match(data)
	}
	return combine(g.Operator, matches)
}

func compileConditions(conditions policy.RequestConditions) (compiledConditions, bool, error) {
	result := compiledConditions{operator: conditions.GroupOperator}
	inspectBody := false
	for _, group := range conditions.Groups {
		rules, body, err := compileRules(group.Rules)
		if err != nil {
			return result, false, err
		}
		inspectBody = inspectBody || body
		result.groups = append(result.groups, compiledConditionGroup{operator: group.Operator, rules: rules})
	}
	return result, inspectBody, nil
}

func compileRules(rules []policy.WAFRequestRule) ([]compiledRule, bool, error) {
	result := make([]compiledRule, len(rules))
	inspectBody := false
	for index, rule := range rules {
		result[index].WAFRequestRule = rule
		inspectBody = inspectBody || rule.Field == "BODY"
		if rule.Operator == "REGEX" {
			pattern := rule.Value
			if !rule.CaseSensitive {
				pattern = "(?i)" + pattern
			}
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return nil, false, err
			}
			result[index].regex = compiled
		}
		for _, value := range rule.Values {
			if rule.Operator != "CIDR" {
				break
			}
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				return nil, false, err
			}
			result[index].cidrs = append(result[index].cidrs, prefix)
		}
	}
	return result, inspectBody, nil
}

func (c compiledConditions) match(data requestData) bool {
	if len(c.groups) == 0 {
		return true
	}
	groupMatches := make([]bool, len(c.groups))
	for groupIndex, group := range c.groups {
		ruleMatches := make([]bool, len(group.rules))
		for ruleIndex := range group.rules {
			ruleMatches[ruleIndex] = group.rules[ruleIndex].match(data)
		}
		groupMatches[groupIndex] = combine(group.operator, ruleMatches)
	}
	return combine(c.operator, groupMatches)
}

func (r compiledRule) match(data requestData) bool {
	actual := requestValue(r.WAFRequestRule, data)
	expected := r.Value
	if !r.CaseSensitive && r.Operator != "REGEX" && r.Operator != "CIDR" {
		actual, expected = strings.ToLower(actual), strings.ToLower(expected)
	}

	matched := false
	switch r.Operator {
	case "EXISTS":
		matched = actual != ""
	case "EQUALS":
		matched = actual == expected
	case "CONTAINS":
		matched = strings.Contains(actual, expected)
	case "PREFIX":
		matched = strings.HasPrefix(actual, expected)
	case "SUFFIX":
		matched = strings.HasSuffix(actual, expected)
	case "REGEX":
		matched = r.regex != nil && r.regex.MatchString(actual)
	case "IN":
		for _, candidate := range r.Values {
			if !r.CaseSensitive {
				candidate = strings.ToLower(candidate)
			}
			if actual == candidate {
				matched = true
				break
			}
		}
	case "CIDR":
		address, err := netip.ParseAddr(actual)
		if err == nil {
			for _, prefix := range r.cidrs {
				if prefix.Contains(address) {
					matched = true
					break
				}
			}
		}
	}
	if r.Negate {
		return !matched
	}
	return matched
}

func requestValue(rule policy.WAFRequestRule, data requestData) string {
	r := data.request
	switch rule.Field {
	case "METHOD":
		return r.Method
	case "HOST":
		return r.Host
	case "PATH":
		return r.URL.Path
	case "RAW_QUERY":
		return r.URL.RawQuery
	case "QUERY":
		return r.URL.Query().Get(rule.Name)
	case "HEADER":
		return r.Header.Get(rule.Name)
	case "COOKIE":
		cookie, err := r.Cookie(rule.Name)
		if err == nil {
			return cookie.Value
		}
	case "BODY":
		return data.body
	case "CLIENT_IP":
		return data.ip
	case "USER_AGENT":
		return r.UserAgent()
	}
	return ""
}

func rateKey(rule policy.RateLimitRule, data requestData) string {
	switch rule.Key {
	case "CLIENT_IP":
		return data.ip
	case "CLIENT_IP_PATH":
		return data.ip + "\x00" + data.request.URL.Path
	case "HEADER":
		if value := data.request.Header.Get(rule.KeyName); value != "" {
			return value
		}
		return "missing:" + data.ip
	case "COOKIE":
		cookie, err := data.request.Cookie(rule.KeyName)
		if err == nil {
			return cookie.Value
		}
		return "missing:" + data.ip
	case "GLOBAL":
		return "global"
	}
	return ""
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return strings.Trim(r.RemoteAddr, "[]")
}

func combine(operator string, values []bool) bool {
	if operator == "OR" {
		for _, value := range values {
			if value {
				return true
			}
		}
		return false
	}
	for _, value := range values {
		if !value {
			return false
		}
	}
	return true
}

var presetPatterns = map[string]*regexp.Regexp{
	"SQL_INJECTION":     regexp.MustCompile(`(?i)(?:\bunion\b.{0,24}\bselect\b|\bselect\b.{0,24}\bfrom\b|\bor\b\s+['"]?\d+['"]?\s*=\s*['"]?\d+|(?:--|#|/\*)\s*$|\bsleep\s*\(|\bbenchmark\s*\()`),
	"XSS":               regexp.MustCompile(`(?i)(?:<\s*script\b|javascript\s*:|on(?:error|load|click|mouseover)\s*=|<\s*(?:iframe|object|embed|svg)\b)`),
	"PATH_TRAVERSAL":    regexp.MustCompile(`(?i)(?:\.\./|\.\.\\|%2e%2e(?:%2f|/|%5c|\\)|%252e%252e)`),
	"COMMAND_INJECTION": regexp.MustCompile(`(?i)(?:[;&|\x60]\s*(?:sh|bash|cmd|powershell|curl|wget|nc)\b|\$\([^)]{1,200}\)|\b(?:cat|chmod|chown)\s+/(?:etc|proc|sys)/)`),
	"SCANNER":           regexp.MustCompile(`(?i)(?:/\.env(?:$|[/?])|/\.git(?:$|[/?])|/wp-admin(?:$|[/?])|/wp-login\.php|/phpmyadmin(?:$|[/?])|/server-status(?:$|[/?]))`),
	"BAD_BOTS":          regexp.MustCompile(`(?i)(?:sqlmap|nikto|nuclei|masscan|zgrab|acunetix|nessus|metasploit|dirbuster|gobuster)`),
}

func matchPreset(preset string, data requestData) bool {
	pattern := presetPatterns[preset]
	if pattern == nil {
		return false
	}
	switch preset {
	case "PATH_TRAVERSAL", "SCANNER":
		return pattern.MatchString(data.request.URL.EscapedPath() + "?" + data.request.URL.RawQuery)
	case "BAD_BOTS":
		return pattern.MatchString(data.request.UserAgent())
	default:
		values := make([]string, 0)
		for _, items := range data.request.URL.Query() {
			values = append(values, items...)
		}
		return pattern.MatchString(strings.Join(values, "\n") + "\n" + data.body)
	}
}

type counterStore struct {
	mu         sync.Mutex
	entries    map[string]counter
	operations uint64
}

type counter struct {
	windowStart time.Time
	count       int
	bannedUntil time.Time
	lastSeen    time.Time
}

var limiterRegistry sync.Map

func limiterFor(siteID, ruleID string) *counterStore {
	key := siteID + "\x00" + ruleID
	value, _ := limiterRegistry.LoadOrStore(key, &counterStore{entries: map[string]counter{}})
	return value.(*counterStore)
}

func (s *counterStore) allow(key string, now time.Time, rule policy.RateLimitRule) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	window := time.Duration(rule.WindowSeconds) * time.Second
	entry := s.entries[key]
	entry.lastSeen = now
	if now.Before(entry.bannedUntil) {
		s.entries[key] = entry
		return false, entry.bannedUntil.Sub(now)
	}
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= window {
		entry.windowStart, entry.count = now, 0
	}
	limit := rule.Requests + rule.Burst
	if entry.count >= limit {
		retry := window - now.Sub(entry.windowStart)
		if rule.BanSeconds > 0 {
			entry.bannedUntil = now.Add(time.Duration(rule.BanSeconds) * time.Second)
			retry = time.Duration(rule.BanSeconds) * time.Second
		}
		s.entries[key] = entry
		return false, retry
	}
	entry.count++
	s.entries[key] = entry
	s.operations++
	if s.operations%1024 == 0 {
		for candidate, value := range s.entries {
			if now.Sub(value.lastSeen) > 2*window && now.After(value.bannedUntil) {
				delete(s.entries, candidate)
			}
		}
	}
	return true, 0
}

var _ caddy.Provisioner = (*Handler)(nil)
var _ caddyhttp.MiddlewareHandler = (*Handler)(nil)
