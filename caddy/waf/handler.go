package waf

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"hash/fnv"
	"html/template"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/google/uuid"

	"goveto-edge/internal/policy"
)

type Handler struct {
	SiteID          string                 `json:"site_id"`
	ChallengeSecret string                 `json:"challenge_secret,omitempty"`
	WAF             policy.WAFPolicy       `json:"waf"`
	Access          policy.AccessPolicy    `json:"access"`
	RateLimit       policy.RateLimitPolicy `json:"rate_limit"`

	groups         []compiledGroup
	exceptions     []compiledException
	rateRules      []compiledRateRule
	inspectBody    bool
	challengeKey   []byte
	access         compiledAccess
	distributed    distributedStore
	distributedErr error
	autoBan        autoBanStore
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

type compiledException struct {
	ids        map[string]bool
	conditions compiledConditions
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
}

//go:embed templates/*.html templates/*.js
var pageFiles embed.FS

var pageTemplates = template.Must(template.ParseFS(pageFiles, "templates/*.html"))

func init() {
	caddy.RegisterModule(Handler{})
}

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
	if err := h.Access.NormalizeAndValidate(); err != nil {
		return fmt.Errorf("invalid access policy: %w", err)
	}
	compiledAccessPolicy, err := compileAccess(h.Access)
	if err != nil {
		return fmt.Errorf("compile access policy: %w", err)
	}
	h.access = compiledAccessPolicy
	if h.hasCaptchaGroup() {
		key, err := decodeChallengeSecret(h.ChallengeSecret)
		if err != nil {
			return err
		}
		h.challengeKey = key
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
	h.exceptions = make([]compiledException, 0, len(h.WAF.Exceptions))
	for _, exception := range h.WAF.Exceptions {
		if !exception.Enabled {
			continue
		}
		conditions, body, err := compileConditions(exception.Conditions)
		if err != nil {
			return err
		}
		h.inspectBody = h.inspectBody || body
		h.exceptions = append(h.exceptions, compiledException{ids: stringSet(exception.RuleIDs), conditions: conditions})
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
	if h.RateLimit.Backend == "REDIS" || (h.Access.Enabled && h.Access.TemporaryBlocks) || h.hasCaptchaGroup() {
		h.distributed, h.distributedErr = configuredRedisStore()
	}
	if h.WAF.Enabled && h.hasAutoBanGroup() {
		h.autoBan = processLocalAutoBanStore
		if store, err := configuredRedisStore(); err == nil {
			if backend, ok := store.(*redisStore); ok {
				h.autoBan = &redisAutoBanStore{client: backend.client}
			}
		}
	}
	return nil
}

func (h *Handler) Cleanup() error {
	if h.access.geo != nil {
		return h.access.geo.Close()
	}
	return nil
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	requestID := normalizedRequestID(r.Header.Get("X-Request-ID"))
	r.Header.Set("X-Request-ID", requestID)
	w.Header().Set("X-Request-ID", requestID)
	data := requestData{request: r, ip: h.access.clientIP(r)}
	if h.inspectBody && r.Body != nil && h.WAF.MaxBodyBytes > 0 {
		body, err := io.ReadAll(io.LimitReader(r.Body, h.WAF.MaxBodyBytes))
		if err != nil {
			return err
		}
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		data.body = string(body)
	}

	if h.Access.Enabled {
		if h.Access.TemporaryBlocks {
			blocked, retryAfter, err := h.temporaryBlocked(r, data.ip)
			if err != nil && h.Access.TemporaryBlockFailure == "CLOSED" {
				setSecurityEvent(w.Header(), "ERROR", "access:temporary-block-backend", "access", "backend_unavailable")
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return nil
			}
			if blocked {
				setSecurityEvent(w.Header(), "BLOCK", "access:temporary-block", "access", "temporary_block")
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Seconds()+0.5))))
				http.Error(w, http.StatusText(h.Access.StatusCode), h.Access.StatusCode)
				return nil
			}
		}
		if decision := h.access.match(r, data.ip); decision != nil {
			action := "BLOCK"
			if h.Access.Mode == "MONITOR" {
				action = "MONITOR"
			}
			setSecurityEvent(w.Header(), action, decision.ruleID, "access", decision.reason)
			if action == "BLOCK" {
				http.Error(w, http.StatusText(h.Access.StatusCode), h.Access.StatusCode)
				return nil
			}
		}
	}

	// Auto-ban enforcement is skipped entirely in WAF monitor mode so an
	// observation-only deployment cannot hard-block clients.
	if h.autoBan != nil && h.WAF.Mode != policy.WAFModeMonitor {
		if blocked, retry, err := h.autoBan.Blocked(r.Context(), h.SiteID, data.ip); err == nil && blocked {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Seconds()+0.5))))
			setSecurityEvent(w.Header(), "BLOCK", "waf:auto-ban", "waf", "auto_ban")
			http.Error(w, http.StatusText(h.WAF.BlockStatus), h.WAF.BlockStatus)
			return nil
		}
	}

	if h.WAF.Enabled && inRollout(h.WAF.RolloutPercentage, h.SiteID+":waf", data) {
		if decision := h.matchWAF(data); decision != nil {
			if decision.action == policy.WAFActionAllow {
				return next.ServeHTTP(w, r)
			}
			if decision.action == policy.WAFActionMonitor || h.WAF.Mode == policy.WAFModeMonitor {
				setSecurityEvent(w.Header(), "MONITOR", decision.id, decision.source, decision.match)
			} else if decision.action == policy.WAFActionTag {
				appendTag(r.Header, "X-Goveto-WAF-Tags", decision.tag)
				setSecurityEvent(w.Header(), "TAG", decision.id, decision.source, decision.match)
				w.Header().Set("X-Goveto-WAF-Tag", decision.tag)
			} else if decision.action == policy.WAFActionCaptcha && h.hasClearance(r, decision.id, data.ip) {
				setSecurityEvent(w.Header(), "CAPTCHA-PASS", decision.id, decision.source, decision.match)
			} else {
				if autoBanCountsHit(decision.action, h.WAF.Mode) {
					h.recordAutoBan(r.Context(), decision.id, data.ip)
				}
				return h.executeDecision(w, r, data.ip, *decision)
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
			allowed, retryAfter, limiterErr := h.allowRate(r, rule, key, now)
			if limiterErr != nil {
				setSecurityEvent(w.Header(), "ERROR", rule.ID, "rate_limit", "backend_unavailable")
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return nil
			}
			if allowed {
				continue
			}
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Seconds()+0.5))))
			setSecurityEvent(w.Header(), "RATE_LIMIT", rule.ID, "rate_limit", rule.Key)
			http.Error(w, http.StatusText(rule.StatusCode), rule.StatusCode)
			return nil
		}
	}

	return next.ServeHTTP(w, r)
}

func (h Handler) matchWAF(data requestData) *wafDecision {
	for _, group := range h.groups {
		if group.Action != policy.WAFActionAllow {
			continue
		}
		if !inRollout(group.RolloutPercentage, h.SiteID+":"+group.ID, data) || h.excepted(group.ID, data) {
			continue
		}
		if matched, detail := group.match(data); matched {
			return decisionForGroup(group, detail)
		}
	}
	for _, preset := range h.WAF.Presets {
		ruleID := "preset:" + preset
		if !h.excepted(ruleID, data) && matchPreset(preset, data) {
			return &wafDecision{
				id:       ruleID,
				action:   policy.WAFActionShowPage,
				status:   h.WAF.BlockStatus,
				response: h.WAF.BlockResponse,
				source:   h.WAF.Engine + ":" + h.WAF.RuleSetVersion,
				match:    preset,
			}
		}
	}
	for _, group := range h.groups {
		if group.Action == policy.WAFActionAllow {
			continue
		}
		if !inRollout(group.RolloutPercentage, h.SiteID+":"+group.ID, data) || h.excepted(group.ID, data) {
			continue
		}
		if matched, detail := group.match(data); matched {
			return decisionForGroup(group, detail)
		}
	}
	return nil
}

func decisionForGroup(group compiledGroup, match string) *wafDecision {
	return &wafDecision{
		id:             group.ID,
		action:         group.Action,
		status:         group.StatusCode,
		response:       group.Response,
		redirectURL:    group.RedirectURL,
		redirectStatus: group.RedirectStatus,
		tag:            group.Tag,
		source:         "custom",
		match:          match,
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

// autoBanCountsHit reports whether a matched WAF action should accrue toward
// an auto-ban threshold. Monitor mode (engine or group) and soft actions such
// as TAG never write bans; only enforcement actions do.
func autoBanCountsHit(action, wafMode string) bool {
	if wafMode == policy.WAFModeMonitor {
		return false
	}
	switch action {
	case policy.WAFActionBlock, policy.WAFActionShowPage, policy.WAFActionRedirect, policy.WAFActionCaptcha:
		return true
	default:
		return false
	}
}

// recordAutoBan accrues a hit for the matched group's auto-ban policy. When
// the configured threshold is reached within the window the store writes a
// temporary block; subsequent requests are rejected before re-evaluating the
// WAF (unless the engine is in monitor mode).
func (h Handler) recordAutoBan(ctx context.Context, groupID, ip string) {
	if h.autoBan == nil {
		return
	}
	for _, group := range h.WAF.Groups {
		if group.ID != groupID || !group.AutoBan.Enabled {
			continue
		}
		if _, _, err := h.autoBan.RecordHit(ctx, h.SiteID, groupID, ip, group.AutoBan); err != nil {
			// A Redis hiccup must not suppress the WAF verdict itself; the hit
			// is simply lost and the next request tries again.
			break
		}
		break
	}
}

func (h Handler) excepted(ruleID string, data requestData) bool {
	for _, exception := range h.exceptions {
		if (exception.ids[ruleID] || exception.ids["*"]) && exception.conditions.match(data) {
			return true
		}
	}
	return false
}

func inRollout(percentage int, salt string, data requestData) bool {
	if percentage >= 100 {
		return true
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(salt))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(data.ip))
	return int(hash.Sum32()%100) < percentage
}

func setSecurityEvent(header http.Header, action, ruleID, source, match string) {
	header.Set("X-Goveto-WAF", action)
	header.Set("X-Goveto-WAF-Rule", ruleID)
	header.Set("X-Goveto-WAF-Source", source)
	header.Set("X-Goveto-WAF-Match", match)
}

func normalizedRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return uuid.NewString()
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && !strings.ContainsRune("._:-", character) {
			return uuid.NewString()
		}
	}
	return value
}

func (h Handler) temporaryBlocked(r *http.Request, ip string) (bool, time.Duration, error) {
	if h.distributedErr != nil {
		return false, 0, h.distributedErr
	}
	if h.distributed == nil {
		return false, 0, fmt.Errorf("temporary block backend is unavailable")
	}
	return h.distributed.Blocked(r.Context(), h.SiteID, ip)
}

func (h Handler) allowRate(r *http.Request, rule *compiledRateRule, key string, now time.Time) (bool, time.Duration, error) {
	if h.RateLimit.Backend != "REDIS" {
		allowed, retry := rule.limiter.allow(key, now, rule.RateLimitRule)
		return allowed, retry, nil
	}
	if h.distributedErr == nil && h.distributed != nil {
		allowed, retry, err := h.distributed.Allow(r.Context(), h.SiteID, rule.ID, key, rule.RateLimitRule)
		if err == nil {
			return allowed, retry, nil
		}
		h.distributedErr = err
	}
	switch h.RateLimit.FailureMode {
	case "OPEN":
		return true, 0, nil
	case "LOCAL":
		allowed, retry := rule.limiter.allow(key, now, rule.RateLimitRule)
		return allowed, retry, nil
	default:
		return false, 0, fmt.Errorf("Redis rate-limit backend is unavailable: %w", h.distributedErr)
	}
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
	values := header.Values(name)
	for _, value := range values {
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

// match evaluates the group's rules against the request and reports whether
// the group matched plus a human-readable summary of every rule that fired,
// e.g. "rule[2]:PATH:EQUALS,rule[3]:QUERY:REGEX". OR and AND groups both
// list all matched rule indices so operators can see which sub-rules tripped.
// This replaces the opaque "conditions" match string.
func (g compiledGroup) match(data requestData) (bool, string) {
	matches := make([]bool, len(g.rules))
	for index := range g.rules {
		matches[index] = g.rules[index].match(data)
	}
	if !combine(g.Operator, matches) {
		return false, ""
	}
	var detail strings.Builder
	for index, matched := range matches {
		if !matched {
			continue
		}
		if detail.Len() > 0 {
			detail.WriteByte(',')
		}
		rule := g.rules[index].WAFRequestRule
		fmt.Fprintf(&detail, "rule[%d]:%s:%s", index+1, rule.Field, rule.Operator)
	}
	return true, detail.String()
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
	case "PATH":
		return data.request.URL.Path
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
var _ caddy.CleanerUpper = (*Handler)(nil)
var _ caddyhttp.MiddlewareHandler = (*Handler)(nil)
