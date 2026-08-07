package cachematch

import (
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"goveto-edge/internal/cacherange"
	"goveto-edge/internal/policy"
)

type Matcher struct {
	Conditions         policy.CacheConditions `json:"conditions"`
	CacheRangeRequests bool                   `json:"cache_range_requests"`
	BypassCacheControl []string               `json:"bypass_cache_control,omitempty"`
	compiled           [][]*regexp.Regexp
}

func init() { caddy.RegisterModule(Matcher{}) }

func (Matcher) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.matchers.goveto_cache", New: func() caddy.Module { return new(Matcher) }}
}

func (m *Matcher) Provision(_ caddy.Context) error {
	m.compiled = make([][]*regexp.Regexp, len(m.Conditions.Groups))
	for gi, group := range m.Conditions.Groups {
		m.compiled[gi] = make([]*regexp.Regexp, len(group.Rules))
		for ri, rule := range group.Rules {
			if rule.Type == "PATH_REGEX" {
				compiled, err := regexp.Compile(rule.Value)
				if err != nil {
					return err
				}
				m.compiled[gi][ri] = compiled
			}
		}
	}
	return nil
}

func (m Matcher) Match(r *http.Request) bool {
	if matchesCacheControl(r.Header.Values("Cache-Control"), m.BypassCacheControl) {
		return false
	}
	if value := strings.Join(r.Header.Values("Range"), ","); value != "" {
		spec, ok := parseCacheableRange(value)
		if !m.CacheRangeRequests || r.Header.Get("If-Range") != "" || !ok {
			return false
		}
		*r = *r.WithContext(cacherange.WithContext(r.Context(), spec))
	}
	groupMatches := make([]bool, len(m.Conditions.Groups))
	for gi, group := range m.Conditions.Groups {
		ruleMatches := make([]bool, len(group.Rules))
		for ri, rule := range group.Rules {
			ruleMatches[ri] = m.matchRule(r.URL.Path, rule, m.compiled[gi][ri])
		}
		groupMatches[gi] = combine(group.Operator, ruleMatches)
	}
	return combine(m.Conditions.GroupOperator, groupMatches)
}

func matchesCacheControl(values, configured []string) bool {
	for _, part := range strings.Split(strings.Join(values, ","), ",") {
		part = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(part), " ", ""))
		name := strings.SplitN(part, "=", 2)[0]
		for _, candidate := range configured {
			candidate = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(candidate), " ", ""))
			if part == candidate || (!strings.Contains(candidate, "=") && name == candidate) {
				return true
			}
		}
	}
	return false
}

func cacheableRange(value string) bool {
	_, ok := parseCacheableRange(value)
	return ok
}

func parseCacheableRange(value string) (cacherange.Spec, bool) {
	unit, interval, ok := strings.Cut(strings.TrimSpace(value), "=")
	if !ok || !strings.EqualFold(strings.TrimSpace(unit), "bytes") || strings.Contains(interval, ",") {
		return cacherange.Spec{}, false
	}
	startText, endText, ok := strings.Cut(strings.TrimSpace(interval), "-")
	if !ok {
		return cacherange.Spec{}, false
	}
	startText = strings.TrimSpace(startText)
	endText = strings.TrimSpace(endText)
	// Suffix range: bytes=-500 selects the final 500 bytes of the
	// representation. The bound is resolved against the content length when
	// the cached body is served, because the matcher does not know the
	// representation length up front.
	if startText == "" {
		if endText == "" {
			return cacherange.Spec{}, false
		}
		suffix, err := strconv.ParseUint(endText, 10, 64)
		return cacherange.Spec{SuffixLength: suffix}, err == nil && suffix > 0
	}
	start, err := strconv.ParseUint(startText, 10, 64)
	if err != nil {
		return cacherange.Spec{}, false
	}
	if endText == "" {
		// Open-ended range (bytes=500-) is not cacheable: the response has no
		// fixed end bound and cannot be served from a stored full representation.
		return cacherange.Spec{}, false
	}
	end, err := strconv.ParseUint(endText, 10, 64)
	return cacherange.Spec{Start: start, End: end}, err == nil && end >= start
}

func (Matcher) matchRule(requestPath string, rule policy.CacheConditionRule, compiled *regexp.Regexp) bool {
	switch rule.Type {
	case "ALL":
		return true
	case "EXTENSION":
		extension := strings.TrimPrefix(strings.ToLower(path.Ext(requestPath)), ".")
		for _, candidate := range rule.Values {
			if extension == candidate {
				return true
			}
		}
	case "PATH_PREFIX":
		for _, candidate := range rule.Values {
			if strings.HasPrefix(requestPath, candidate) {
				return true
			}
		}
	case "PATH_REGEX":
		return compiled != nil && compiled.MatchString(requestPath)
	}
	return false
}

func combine(operator string, values []bool) bool {
	if operator == "AND" {
		for _, value := range values {
			if !value {
				return false
			}
		}
		return true
	}
	for _, value := range values {
		if value {
			return true
		}
	}
	return false
}

var _ caddy.Provisioner = (*Matcher)(nil)
var _ caddyhttp.RequestMatcher = (*Matcher)(nil)
