package waf

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/google/uuid"
	"github.com/oschwald/geoip2-golang"
	"go.uber.org/zap"

	"goveto-edge/internal/policy"
)

const wafRequestBodyLimit = 64 << 10

const (
	rateBackendInitialBackoff = time.Second
	rateBackendMaxBackoff     = 30 * time.Second
)

type Handler struct {
	SiteID          string           `json:"site_id"`
	ChallengeSecret string           `json:"challenge_secret,omitempty"`
	WAF             policy.WAFPolicy `json:"waf"`

	ruleSets       []compiledRuleSet
	inspectBody    bool
	challengeKey   []byte
	trustedProxies []netip.Prefix
	geo            *geoip2.Reader
	distributed    distributedStore
	distributedErr error
	rateBackend    *rateBackendState
	logger         *zap.Logger
}

type rateBackendState struct {
	mu         sync.Mutex
	store      distributedStore
	err        error
	retryAt    time.Time
	failures   int
	probing    bool
	generation uint64
}

type compiledRuleSet struct {
	ID    string
	Name  string
	rules []compiledRule
}

type compiledRule struct {
	policy.WAFRule
	conditions compiledConditions
	limiter    *counterStore
}

type compiledConditions struct {
	operator string
	groups   []compiledConditionGroup
}

type compiledConditionGroup struct {
	operator   string
	conditions []compiledCondition
}

type compiledCondition struct {
	policy.WAFCondition
	regex *regexp.Regexp
	cidrs []netip.Prefix
}

type requestData struct {
	request *http.Request
	body    string
	ip      string
	country string
	region  string
}

type wafDecision struct {
	id             string
	action         string
	status         int
	response       policy.WAFResponse
	redirectURL    string
	redirectStatus int
	tag            string
	source         string
	match          string
	retry          time.Duration
}

type rateBackendError struct {
	ruleID string
	err    error
}

func (e *rateBackendError) Error() string { return e.err.Error() }
func (e *rateBackendError) Unwrap() error { return e.err }

//go:embed templates/*.html templates/*.js
var pageFiles embed.FS

var pageTemplates = template.Must(template.ParseFS(pageFiles, "templates/*.html"))

func init() { caddy.RegisterModule(Handler{}) }

func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.goveto_waf", New: func() caddy.Module { return new(Handler) }}
}

func (h *Handler) Provision(ctx caddy.Context) error {
	h.logger = ctx.Logger(h)
	h.distributed = nil
	h.distributedErr = nil
	h.rateBackend = nil
	h.trustedProxies = nil
	h.inspectBody = false
	if h.SiteID == "" {
		return fmt.Errorf("site_id is required")
	}
	if err := h.WAF.NormalizeAndValidate(); err != nil {
		return fmt.Errorf("invalid WAF policy: %w", err)
	}
	if h.WAF.TrustedProxyChain {
		h.trustedProxies = make([]netip.Prefix, 0, len(h.WAF.TrustedProxies))
		for _, value := range h.WAF.TrustedProxies {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				return fmt.Errorf("compile trusted proxy %q: %w", value, err)
			}
			h.trustedProxies = append(h.trustedProxies, prefix)
		}
	}
	if h.WAF.NeedsGeoIP() {
		if h.WAF.GeoIPDatabase == "" {
			return errors.New("GeoIP database is required for COUNTRY or REGION rules")
		}
		geo, err := geoip2.Open(h.WAF.GeoIPDatabase)
		if err != nil {
			return fmt.Errorf("open WAF GeoIP database: %w", err)
		}
		h.geo = geo
	}
	if h.hasCaptchaRule() {
		key, err := decodeChallengeSecret(h.ChallengeSecret)
		if err != nil {
			return err
		}
		h.challengeKey = key
	}
	h.ruleSets = make([]compiledRuleSet, 0, len(h.WAF.RuleSets))
	needsRedisRate := false
	for _, set := range h.WAF.RuleSets {
		if !set.Enabled {
			continue
		}
		compiledSet := compiledRuleSet{ID: set.ID, Name: set.Name}
		for _, rule := range set.Rules {
			if !rule.Enabled {
				continue
			}
			compiled, err := compileRule(rule)
			if err != nil {
				return fmt.Errorf("compile rule %s: %w", rule.ID, err)
			}
			if rule.Type == policy.WAFRuleTypeRateLimit {
				compiled.limiter = limiterFor(h.SiteID, rule.ID)
				if rule.Backend == "REDIS" {
					needsRedisRate = true
				}
			}
			for _, group := range rule.Conditions.Groups {
				for _, condition := range group.Conditions {
					h.inspectBody = h.inspectBody || condition.Field == "BODY"
				}
			}
			compiledSet.rules = append(compiledSet.rules, compiled)
		}
		h.ruleSets = append(h.ruleSets, compiledSet)
	}
	if needsRedisRate || h.hasCaptchaRule() {
		h.distributed, h.distributedErr = configuredRedisStore()
	}
	if needsRedisRate {
		h.rateBackend = &rateBackendState{store: h.distributed}
		if h.distributedErr != nil {
			h.rateBackend.err = h.distributedErr
			h.rateBackend.failures = 1
			h.rateBackend.retryAt = time.Now().Add(rateBackendInitialBackoff)
			if h.logger != nil {
				h.logger.Warn("Redis WAF rate-limit backend unavailable",
					zap.String("site_id", h.SiteID), zap.Error(h.distributedErr))
			}
		}
	}
	return nil
}

func (h *Handler) Cleanup() error {
	if h.geo != nil {
		return h.geo.Close()
	}
	return nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	requestID := normalizedRequestID(r.Header.Get("X-Request-ID"))
	r.Header.Set("X-Request-ID", requestID)
	w.Header().Set("X-Request-ID", requestID)
	data := requestData{request: r, ip: h.clientIP(r)}
	if h.geo != nil {
		if ip, err := parseAddress(data.ip); err == nil {
			if record, lookupErr := h.geo.City(net.IP(ip.AsSlice())); lookupErr == nil {
				data.country = strings.ToUpper(record.Country.IsoCode)
				if len(record.Subdivisions) > 0 {
					data.region = strings.ToUpper(record.Subdivisions[0].IsoCode)
					if data.country != "" && data.region != "" {
						data.region = data.country + "-" + data.region
					}
				}
			}
		}
	}
	if h.WAF.Enabled && h.inspectBody && r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, wafRequestBodyLimit+1))
		if err != nil {
			return err
		}
		if len(body) > wafRequestBodyLimit {
			_ = r.Body.Close()
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			setSecurityEvent(w.Header(), "BLOCK", "request-body-limit", "body_inspection", "body_too_large")
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return nil
		}
		if err = r.Body.Close(); err != nil {
			return err
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		data.body = string(body)
	}

	if h.WAF.Enabled {
		soft, terminal, err := h.evaluateWAF(data)
		if err != nil {
			var backendErr *rateBackendError
			if errors.As(err, &backendErr) {
				setSecurityEvent(w.Header(), "ERROR", backendErr.ruleID, "rate_limit", "backend_unavailable")
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return nil
			}
			return err
		}
		for _, decision := range soft {
			if decision.action == policy.WAFActionTag {
				appendTag(r.Header, "X-Goveto-WAF-Tags", decision.tag)
				appendTag(w.Header(), "X-Goveto-WAF-Tags", decision.tag)
			}
		}
		if terminal != nil {
			if terminal.retry > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(terminal.retry.Seconds()+0.5))))
			}
			if terminal.action == policy.WAFActionAllow {
				setSecurityEvent(w.Header(), "ALLOW", terminal.id, terminal.source, terminal.match)
				return next.ServeHTTP(w, r)
			}
			if terminal.action == policy.WAFActionCaptcha && h.hasClearance(r, terminal.id, data.ip) {
				setSecurityEvent(w.Header(), "CAPTCHA-PASS", terminal.id, terminal.source, terminal.match)
				return next.ServeHTTP(w, r)
			}
			return h.executeDecision(w, r, data.ip, *terminal)
		}
		if len(soft) > 0 {
			decision := soft[0]
			setSecurityEvent(w.Header(), decision.action, decision.id, decision.source, decision.match)
		}
	}
	return next.ServeHTTP(w, r)
}

func (h *Handler) evaluateWAF(data requestData) ([]wafDecision, *wafDecision, error) {
	soft := make([]wafDecision, 0, 2)
	for setIndex := range h.ruleSets {
		set := &h.ruleSets[setIndex]
		for ruleIndex := range set.rules {
			rule := &set.rules[ruleIndex]
			matched, detail, retry, err := h.matchRule(data, rule)
			if err != nil {
				return nil, nil, err
			}
			if !matched {
				continue
			}
			decision := decisionForRule(*set, *rule, detail, retry)
			if decision.action == policy.WAFActionMonitor || decision.action == policy.WAFActionTag {
				soft = append(soft, decision)
				continue
			}
			return soft, &decision, nil
		}
	}
	return soft, nil, nil
}

func (h *Handler) matchRule(data requestData, rule *compiledRule) (bool, string, time.Duration, error) {
	if rule.Type == policy.WAFRuleTypeRateLimit {
		if len(rule.conditions.groups) > 0 {
			matched, detail := rule.conditions.match(data)
			if !matched {
				return false, "", 0, nil
			}
			_ = detail
		}
		key := rateKey(rule.WAFRule, data)
		allowed, retry, err := h.allowRate(data.request, rule, key, time.Now())
		if err != nil {
			return false, "", 0, &rateBackendError{ruleID: rule.ID, err: err}
		}
		return !allowed, "rate=" + rule.Key, retry, nil
	}
	matched, detail := rule.conditions.match(data)
	return matched, detail, 0, nil
}

func (h *Handler) allowRate(r *http.Request, rule *compiledRule, key string, now time.Time) (bool, time.Duration, error) {
	if rule.Backend != "REDIS" {
		allowed, retry := rule.limiter.allow(key, now, rule.WAFRule)
		return allowed, retry, nil
	}

	store, backendErr, probe, generation := h.acquireRateBackend(now)
	if backendErr == nil || probe {
		if probe {
			backendErr = nil
		}
		if store == nil {
			store, backendErr = configuredRedisStore()
		}
		if backendErr == nil {
			allowed, retry, err := store.Allow(r.Context(), h.SiteID, rule.ID, key, rule.WAFRule)
			if err == nil {
				if probe {
					h.recoverRateBackend(store, generation, rule.ID)
				}
				return allowed, retry, nil
			}
			backendErr = err
		}
		h.failRateBackend(backendErr, now, generation, probe, rule.ID)
	}
	if backendErr == nil {
		backendErr = errors.New("Redis rate-limit backend is unavailable")
	}
	switch rule.FailureMode {
	case "OPEN":
		return true, 0, nil
	case "LOCAL":
		allowed, retry := rule.limiter.allow(key, now, rule.WAFRule)
		return allowed, retry, nil
	default:
		return false, 0, fmt.Errorf("Redis rate-limit backend is unavailable: %w", backendErr)
	}
}

func (h *Handler) acquireRateBackend(now time.Time) (distributedStore, error, bool, uint64) {
	if h.rateBackend == nil {
		return nil, errors.New("Redis rate-limit backend is not initialized"), false, 0
	}
	state := h.rateBackend
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.err == nil {
		return state.store, nil, false, state.generation
	}
	if state.probing || now.Before(state.retryAt) {
		return nil, state.err, false, state.generation
	}
	state.probing = true
	return state.store, state.err, true, state.generation
}

func (h *Handler) failRateBackend(err error, now time.Time, generation uint64, probe bool, ruleID string) {
	state := h.rateBackend
	state.mu.Lock()
	if state.generation != generation || (!probe && state.err != nil) {
		state.mu.Unlock()
		return
	}
	state.failures++
	state.err = err
	state.retryAt = now.Add(rateBackendBackoff(state.failures))
	state.probing = false
	state.generation++
	delay := state.retryAt.Sub(now)
	state.mu.Unlock()
	if h.logger != nil {
		h.logger.Warn("Redis WAF rate-limit request failed; circuit opened",
			zap.String("site_id", h.SiteID), zap.String("rule_id", ruleID),
			zap.Duration("retry_after", delay), zap.Error(err))
	}
}

func (h *Handler) recoverRateBackend(store distributedStore, generation uint64, ruleID string) {
	state := h.rateBackend
	state.mu.Lock()
	if state.generation != generation || !state.probing {
		state.mu.Unlock()
		return
	}
	state.store = store
	state.err = nil
	state.retryAt = time.Time{}
	state.failures = 0
	state.probing = false
	state.generation++
	state.mu.Unlock()
	if h.logger != nil {
		h.logger.Info("Redis WAF rate-limit backend recovered",
			zap.String("site_id", h.SiteID), zap.String("rule_id", ruleID))
	}
}

func rateBackendBackoff(failures int) time.Duration {
	delay := rateBackendInitialBackoff
	for attempt := 1; attempt < failures && delay < rateBackendMaxBackoff; attempt++ {
		delay *= 2
	}
	return min(delay, rateBackendMaxBackoff)
}

func decisionForRule(set compiledRuleSet, rule compiledRule, detail string, retry time.Duration) wafDecision {
	action := rule.Action
	return wafDecision{
		id: rule.ID, action: action.Type, status: action.StatusCode, response: action.Response,
		redirectURL: action.RedirectURL, redirectStatus: action.RedirectStatus, tag: action.Tag,
		source: "ruleset:" + set.ID, match: detail, retry: retry,
	}
}

func (h Handler) executeDecision(w http.ResponseWriter, r *http.Request, ip string, decision wafDecision) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	setSecurityEvent(w.Header(), decision.action, decision.id, decision.source, decision.match)
	switch decision.action {
	case policy.WAFActionShowPage:
		w.Header().Set("X-Goveto-WAF", "BLOCK")
		writeWAFResponse(w, decision.status, decision.id, decision.response)
	case policy.WAFActionBlock:
		w.Header().Set("X-Goveto-WAF", "BLOCK")
		w.WriteHeader(decision.status)
	case policy.WAFActionRedirect:
		w.Header().Set("X-Goveto-WAF", "REDIRECT")
		http.Redirect(w, r, decision.redirectURL, decision.redirectStatus)
	case policy.WAFActionCaptcha:
		w.Header().Set("X-Goveto-WAF", "CAPTCHA")
		if h.completeChallenge(w, r, decision.id, ip) {
			return nil
		}
		token, err := h.challengeToken(decision.id, r, ip)
		if err != nil {
			var limited *challengeGenerationLimitError
			switch {
			case errors.As(err, &limited):
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(limited.retryAfter.Round(time.Second)/time.Second))))
				w.Header().Set("X-Goveto-WAF-Challenge", "rate_limited")
				http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
				return nil
			case errors.Is(err, errPoWGenerationBusy):
				w.Header().Set("Retry-After", "1")
				w.Header().Set("X-Goveto-WAF-Challenge", "busy")
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return nil
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				return nil
			}
			return err
		}
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; worker-src blob:")
		page, err := renderPage("captcha.html", struct {
			Token        string
			WorkerSource template.JS
		}{Token: token, WorkerSource: powWorkerSourceJSON})
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, page)
	}
	return nil
}

func setSecurityEvent(header http.Header, action, ruleID, source, match string) {
	header.Set("X-Goveto-WAF", action)
	header.Set("X-Goveto-WAF-Rule", ruleID)
	header.Set("X-Goveto-WAF-Source", source)
	header.Set("X-Goveto-WAF-Match", match)
}

func normalizedRequestID(value string) string {
	value = strings.TrimSpace(value)
	if parsed, err := uuid.Parse(value); err == nil {
		return parsed.String()
	}
	return uuid.NewString()
}

func remoteHost(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return host
	}
	return strings.Trim(strings.TrimSpace(address), "[]")
}

func parseAddress(value string) (netip.Addr, error) {
	address, err := netip.ParseAddr(strings.TrimSpace(remoteHost(value)))
	if err != nil {
		return netip.Addr{}, err
	}
	return address.Unmap(), nil
}

func (h *Handler) clientIP(request *http.Request) string {
	direct, err := parseAddress(request.RemoteAddr)
	if err != nil {
		return remoteHost(request.RemoteAddr)
	}
	if !h.WAF.TrustedProxyChain || !containsPrefix(h.trustedProxies, direct) {
		return direct.String()
	}
	chain := strings.Split(strings.Join(request.Header.Values("X-Forwarded-For"), ","), ",")
	current := direct
	for index := len(chain) - 1; index >= 0; index-- {
		candidate, parseErr := parseAddress(chain[index])
		if parseErr != nil {
			return direct.String()
		}
		current = candidate
		if !containsPrefix(h.trustedProxies, candidate) {
			return candidate.String()
		}
	}
	return current.String()
}

func containsPrefix(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func writeWAFResponse(w http.ResponseWriter, status int, ruleID string, response policy.WAFResponse) {
	body := response.Body
	contentType := "text/html; charset=utf-8"
	switch response.Type {
	case policy.WAFResponseHTML:
	case policy.WAFResponseText:
		contentType = "text/plain; charset=utf-8"
	case policy.WAFResponseJSON:
		contentType = "application/json; charset=utf-8"
	default:
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		body, _ = renderPage("block.html", struct {
			Status int
			RuleID string
		}{Status: status, RuleID: ruleID})
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func appendTag(header http.Header, name, tag string) {
	for _, value := range header.Values(name) {
		for _, existing := range strings.Split(value, ",") {
			if strings.TrimSpace(existing) == tag {
				return
			}
		}
	}
	header.Add(name, tag)
}

func renderPage(name string, data any) (string, error) {
	var output bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&output, name, data); err != nil {
		return "", err
	}
	return output.String(), nil
}

func compileRule(rule policy.WAFRule) (compiledRule, error) {
	result := compiledRule{WAFRule: rule}
	conditions, err := compileConditions(rule.Conditions)
	if err != nil {
		return result, err
	}
	result.conditions = conditions
	return result, nil
}

func compileConditions(conditions policy.WAFConditions) (compiledConditions, error) {
	result := compiledConditions{operator: conditions.Operator, groups: make([]compiledConditionGroup, 0, len(conditions.Groups))}
	for _, group := range conditions.Groups {
		compiledGroup := compiledConditionGroup{operator: group.Operator, conditions: make([]compiledCondition, 0, len(group.Conditions))}
		for _, condition := range group.Conditions {
			compiled, err := compileCondition(condition)
			if err != nil {
				return result, err
			}
			compiledGroup.conditions = append(compiledGroup.conditions, compiled)
		}
		result.groups = append(result.groups, compiledGroup)
	}
	return result, nil
}

func compileCondition(condition policy.WAFCondition) (compiledCondition, error) {
	result := compiledCondition{WAFCondition: condition}
	if condition.Operator == "REGEX" {
		pattern := condition.Value
		if !condition.CaseSensitive {
			pattern = "(?i)" + pattern
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return result, err
		}
		result.regex = compiled
	}
	if condition.Operator == "CIDR" {
		for _, value := range condition.Values {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				return result, err
			}
			result.cidrs = append(result.cidrs, prefix)
		}
	}
	return result, nil
}

type requestCandidate struct {
	name      string
	value     string
	sensitive bool
}

func (c compiledConditions) match(data requestData) (bool, string) {
	return evaluateExpression(c.operator, len(c.groups), func(index int) (bool, string) {
		return c.groups[index].match(data)
	})
}

func (g compiledConditionGroup) match(data requestData) (bool, string) {
	return evaluateExpression(g.operator, len(g.conditions), func(index int) (bool, string) {
		return g.conditions[index].match(data)
	})
}

func evaluateExpression(operator string, length int, evaluate func(int) (bool, string)) (bool, string) {
	var firstDetail string
	for index := 0; index < length; index++ {
		matched, detail := evaluate(index)
		if matched && firstDetail == "" {
			firstDetail = detail
		}
		if operator == "OR" && matched {
			return true, detail
		}
		if operator == "AND" && !matched {
			return false, ""
		}
	}
	return operator == "AND", firstDetail
}

func (r compiledCondition) match(data requestData) (bool, string) {
	candidates := requestValues(r.WAFCondition, data)
	for _, candidate := range candidates {
		if r.matchValue(candidate.value) {
			if r.Negate {
				return false, ""
			}
			matchedValue := candidate.value
			if r.Operator == "REGEX" && r.regex != nil {
				matchedValue = r.regex.FindString(candidate.value)
			}
			return true, "variable=" + candidate.name + ";match=" + sanitizeMatchText(matchedValue, candidate.sensitive)
		}
	}
	if r.Negate {
		return true, "negated=" + r.Field
	}
	return false, ""
}

func (r compiledCondition) matchValue(actual string) bool {
	expected := r.Value
	if !r.CaseSensitive && r.Operator != "REGEX" && r.Operator != "CIDR" {
		actual, expected = strings.ToLower(actual), strings.ToLower(expected)
	}
	switch r.Operator {
	case "EXISTS":
		return actual != ""
	case "EQUALS":
		return actual == expected
	case "CONTAINS":
		return strings.Contains(actual, expected)
	case "PREFIX":
		return strings.HasPrefix(actual, expected)
	case "SUFFIX":
		return strings.HasSuffix(actual, expected)
	case "REGEX":
		return r.regex != nil && r.regex.MatchString(actual)
	case "IN":
		for _, candidate := range r.Values {
			if !r.CaseSensitive {
				candidate = strings.ToLower(candidate)
			}
			if actual == candidate {
				return true
			}
		}
	case "CIDR":
		address, err := netip.ParseAddr(actual)
		if err == nil {
			for _, prefix := range r.cidrs {
				if prefix.Contains(address) {
					return true
				}
			}
		}
	}
	return false
}

func requestValues(condition policy.WAFCondition, data requestData) []requestCandidate {
	r := data.request
	single := func(name, value string) []requestCandidate {
		return []requestCandidate{{name: name, value: value, sensitive: sensitiveMatchKey(name)}}
	}
	switch condition.Field {
	case "METHOD":
		return single("METHOD", r.Method)
	case "HOST":
		return single("HOST", r.Host)
	case "PATH":
		return single("PATH", r.URL.Path)
	case "RAW_QUERY":
		return single("RAW_QUERY", r.URL.RawQuery)
	case "QUERY":
		return single("QUERY:"+condition.FieldName, r.URL.Query().Get(condition.FieldName))
	case "QUERY_VALUES":
		query := r.URL.Query()
		result := make([]requestCandidate, 0, len(query))
		for _, name := range sortedQueryNames(query) {
			for _, value := range query[name] {
				result = append(result, requestCandidate{name: "QUERY:" + name, value: value, sensitive: sensitiveMatchKey(name)})
			}
		}
		return result
	case "REQUEST_TARGET":
		result := []requestCandidate{{name: "REQUEST_TARGET", value: r.URL.RequestURI()}, {name: "PATH", value: r.URL.Path}}
		for _, candidate := range requestValues(policy.WAFCondition{Field: "QUERY_VALUES"}, data) {
			result = append(result, candidate)
		}
		return result
	case "HEADER":
		return single("HEADER:"+condition.FieldName, r.Header.Get(condition.FieldName))
	case "COOKIE":
		cookie, err := r.Cookie(condition.FieldName)
		if err == nil {
			return single("COOKIE:"+condition.FieldName, cookie.Value)
		}
		return single("COOKIE:"+condition.FieldName, "")
	case "BODY":
		return single("BODY", data.body)
	case "CLIENT_IP":
		return single("CLIENT_IP", data.ip)
	case "USER_AGENT":
		return single("USER_AGENT", r.UserAgent())
	case "COUNTRY":
		return single("COUNTRY", data.country)
	case "REGION":
		return single("REGION", data.region)
	}
	return nil
}

func rateKey(rule policy.WAFRule, data requestData) string {
	switch rule.Key {
	case "CLIENT_IP":
		return data.ip
	case "CLIENT_IP_PATH":
		return data.ip + "\x00" + data.request.URL.Path
	case "PATH":
		return data.request.URL.Path
	case "HEADER":
		if value := data.request.Header.Get(rule.KeyName); value != "" {
			return value
		}
		return "missing:" + data.ip
	case "COOKIE":
		if cookie, err := data.request.Cookie(rule.KeyName); err == nil {
			return cookie.Value
		}
		return "missing:" + data.ip
	case "GLOBAL":
		return "global"
	}
	return ""
}

var secretMatchKey = regexp.MustCompile(`(?i)(?:authorization|cookie|password|passwd|secret|token|api[-_]?key|session|credential)`)
var longOpaqueValue = regexp.MustCompile(`[A-Za-z0-9_+/=-]{24,}`)

func sensitiveMatchKey(value string) bool { return secretMatchKey.MatchString(value) }

func sanitizeMatchText(value string, sensitive bool) string {
	if sensitive {
		return "[REDACTED]"
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ';' {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	value = longOpaqueValue.ReplaceAllString(value, "[REDACTED]")
	characters := []rune(value)
	if len(characters) > 96 {
		value = string(characters[:96]) + "..."
	}
	return value
}

func sortedQueryNames(values map[string][]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type counterStore struct {
	mu         sync.Mutex
	entries    map[string]counter
	operations uint64
}

const maxLocalRateKeys = 4096

type counter struct {
	windowStart time.Time
	count       int
	lastSeen    time.Time
}

var limiterRegistry sync.Map

func limiterFor(siteID, ruleID string) *counterStore {
	key := siteID + "\x00" + ruleID
	value, _ := limiterRegistry.LoadOrStore(key, &counterStore{entries: map[string]counter{}})
	return value.(*counterStore)
}

func (s *counterStore) allow(key string, now time.Time, rule policy.WAFRule) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	window := time.Duration(rule.WindowSeconds) * time.Second
	entry, exists := s.entries[key]
	if !exists && len(s.entries) >= maxLocalRateKeys {
		var oldestKey string
		var oldestTime time.Time
		for candidate, value := range s.entries {
			if oldestKey == "" || value.lastSeen.Before(oldestTime) {
				oldestKey, oldestTime = candidate, value.lastSeen
			}
		}
		delete(s.entries, oldestKey)
	}
	entry.lastSeen = now
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= window {
		entry.windowStart, entry.count = now, 0
	}
	if entry.count >= rule.Requests+rule.Burst {
		s.entries[key] = entry
		return false, window - now.Sub(entry.windowStart)
	}
	entry.count++
	s.entries[key] = entry
	s.operations++
	if s.operations%1024 == 0 {
		for candidate, value := range s.entries {
			if now.Sub(value.lastSeen) > 2*window {
				delete(s.entries, candidate)
			}
		}
	}
	return true, 0
}

var _ caddy.Provisioner = (*Handler)(nil)
var _ caddy.CleanerUpper = (*Handler)(nil)
var _ caddyhttp.MiddlewareHandler = (*Handler)(nil)
