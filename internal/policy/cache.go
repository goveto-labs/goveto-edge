package policy

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

type CachePolicy struct {
	ResponseHeaders    CacheHeaders `json:"response_headers"`
	DevMode            bool         `json:"dev_mode,omitempty"`
	AllowPurgeMethod   bool         `json:"allow_purge_method"`
	RequestCoalescing  bool         `json:"request_coalescing"`
	CacheRangeRequests bool         `json:"cache_range_requests"`
	MaxBodyBytes       uint64       `json:"max_body_bytes"`
	Stale              CacheStale   `json:"stale"`
	Rules              []CacheRule  `json:"rules"`
	Methods            []string     `json:"methods"`
	CacheKey           CacheKey     `json:"cache_key"`
	BypassCacheControl []string     `json:"bypass_cache_control"`
	SurrogateKeyHeader string       `json:"surrogate_key_header"`
}

type CacheHeaders struct {
	XCache bool `json:"x_cache"`
	Age    bool `json:"age"`
}
type CacheStale struct {
	Enabled                bool `json:"enabled"`
	IfErrorSeconds         int  `json:"if_error_seconds"`
	WhileRevalidateSeconds int  `json:"while_revalidate_seconds"`
}
type CacheTTL struct {
	DefaultSeconds    int            `json:"default_seconds"`
	Status            map[string]int `json:"status"`
	OverrideClientTTL bool           `json:"override_client_ttl"`
	ClientSeconds     int            `json:"client_seconds"`
}
type CacheRule struct {
	Name       string          `json:"name"`
	TTL        CacheTTL        `json:"ttl"`
	Conditions CacheConditions `json:"conditions"`
}

const (
	CacheKeyPartMethod = "METHOD"
	CacheKeyPartScheme = "SCHEME"
	CacheKeyPartHost   = "HOST"
	CacheKeyPartPath   = "PATH"
	CacheKeyPartQuery  = "QUERY"
)

type CacheKey struct {
	Parts   []string `json:"parts"`
	Headers []string `json:"headers"`
	Hash    bool     `json:"hash"`
	Hide    bool     `json:"hide"`
}
type CacheConditions struct {
	GroupOperator string                `json:"group_operator"`
	Groups        []CacheConditionGroup `json:"groups"`
}
type CacheConditionGroup struct {
	Operator string               `json:"operator"`
	Rules    []CacheConditionRule `json:"rules"`
}
type CacheConditionRule struct {
	Type   string   `json:"type"`
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
}

func DefaultCachePolicy() CachePolicy {
	return CachePolicy{
		ResponseHeaders:    CacheHeaders{XCache: true, Age: true},
		RequestCoalescing:  true,
		CacheRangeRequests: true,
		MaxBodyBytes:       64 << 20,
		Stale: CacheStale{
			Enabled: true, IfErrorSeconds: 86400, WhileRevalidateSeconds: 30,
		},
		Rules:   []CacheRule{},
		Methods: []string{http.MethodGet, http.MethodHead},
		CacheKey: CacheKey{
			Parts: []string{
				CacheKeyPartMethod,
				CacheKeyPartHost,
				CacheKeyPartPath,
				CacheKeyPartQuery,
			},
			Headers: []string{},
		},
		BypassCacheControl: []string{"no-store", "private"},
		SurrogateKeyHeader: "Surrogate-Key",
	}
}

func (p *CachePolicy) NormalizeAndValidate() error {
	if p.Stale.Enabled && (p.Stale.IfErrorSeconds < 1 || p.Stale.IfErrorSeconds > 31536000) {
		return errors.New("stale.if_error_seconds must be between 1 and 31536000")
	}
	if p.Stale.Enabled && (p.Stale.WhileRevalidateSeconds < 0 || p.Stale.WhileRevalidateSeconds > 31536000) {
		return errors.New("stale.while_revalidate_seconds must be between 0 and 31536000")
	}
	if p.MaxBodyBytes < 1 || p.MaxBodyBytes > 4<<30 {
		return errors.New("max_body_bytes must be between 1 and 4294967296")
	}

	if len(p.Methods) == 0 || len(p.Methods) > 8 {
		return errors.New("methods must contain between 1 and 8 values")
	}
	allowedMethods := map[string]struct{}{
		http.MethodGet: {}, http.MethodHead: {}, http.MethodPost: {}, http.MethodPut: {},
		http.MethodPatch: {}, http.MethodDelete: {}, http.MethodOptions: {},
	}
	seenMethods := map[string]struct{}{}
	for i, method := range p.Methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if _, ok := allowedMethods[method]; !ok {
			return fmt.Errorf("unsupported cache method %q", method)
		}
		if _, ok := seenMethods[method]; ok {
			return fmt.Errorf("duplicate cache method %q", method)
		}
		seenMethods[method] = struct{}{}
		p.Methods[i] = method
	}
	sort.Strings(p.Methods)
	for _, method := range p.Methods {
		if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
			p.RequestCoalescing = false
			break
		}
	}

	allowedKeyParts := map[string]struct{}{
		CacheKeyPartMethod: {},
		CacheKeyPartScheme: {},
		CacheKeyPartHost:   {},
		CacheKeyPartPath:   {},
		CacheKeyPartQuery:  {},
	}
	keyPartOrder := map[string]int{
		CacheKeyPartMethod: 0,
		CacheKeyPartScheme: 1,
		CacheKeyPartHost:   2,
		CacheKeyPartPath:   3,
		CacheKeyPartQuery:  4,
	}
	seenKeyParts := map[string]struct{}{}
	for i, part := range p.CacheKey.Parts {
		part = strings.ToUpper(strings.TrimSpace(part))
		if _, ok := allowedKeyParts[part]; !ok {
			return fmt.Errorf("unsupported cache key part %q", part)
		}
		if _, ok := seenKeyParts[part]; ok {
			return fmt.Errorf("duplicate cache key part %q", part)
		}
		seenKeyParts[part] = struct{}{}
		p.CacheKey.Parts[i] = part
	}
	if _, ok := seenKeyParts[CacheKeyPartPath]; !ok {
		return errors.New("cache_key.parts must include PATH")
	}
	sort.Slice(p.CacheKey.Parts, func(i, j int) bool {
		return keyPartOrder[p.CacheKey.Parts[i]] < keyPartOrder[p.CacheKey.Parts[j]]
	})

	seenHeaders := map[string]struct{}{}
	normalizedHeaders := make([]string, 0, len(p.CacheKey.Headers))
	headerNamePattern := regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	for _, header := range p.CacheKey.Headers {
		header = http.CanonicalHeaderKey(strings.TrimSpace(header))
		if !headerNamePattern.MatchString(header) {
			return fmt.Errorf("invalid cache key header %q", header)
		}
		// Content coding is negotiated by the compression layer and response Vary handling.
		if strings.EqualFold(header, "Accept-Encoding") {
			continue
		}
		if _, ok := seenHeaders[header]; ok {
			return fmt.Errorf("duplicate cache key header %q", header)
		}
		seenHeaders[header] = struct{}{}
		normalizedHeaders = append(normalizedHeaders, header)
	}
	sort.Strings(normalizedHeaders)
	p.CacheKey.Headers = normalizedHeaders

	seenDirectives := map[string]struct{}{}
	directivePattern := regexp.MustCompile(`^[a-z][a-z0-9_-]*(?:=[a-z0-9._-]+)?$`)
	for i, directive := range p.BypassCacheControl {
		directive = strings.ToLower(strings.TrimSpace(directive))
		if strings.ContainsAny(directive, " \t\r\n") {
			return fmt.Errorf("invalid bypass Cache-Control value %q", directive)
		}
		if !directivePattern.MatchString(directive) {
			return fmt.Errorf("invalid bypass Cache-Control value %q", directive)
		}
		if _, ok := seenDirectives[directive]; ok {
			return fmt.Errorf("duplicate bypass Cache-Control value %q", directive)
		}
		seenDirectives[directive] = struct{}{}
		p.BypassCacheControl[i] = directive
	}
	sort.Strings(p.BypassCacheControl)

	p.SurrogateKeyHeader = http.CanonicalHeaderKey(strings.TrimSpace(p.SurrogateKeyHeader))
	if p.SurrogateKeyHeader == "" {
		p.SurrogateKeyHeader = "Surrogate-Key"
	}
	if p.SurrogateKeyHeader != "Surrogate-Key" {
		return errors.New("surrogate_key_header must be Surrogate-Key")
	}

	if len(p.Rules) > 32 {
		return errors.New("rules must contain at most 32 cache rules")
	}
	seenNames := map[string]struct{}{}
	for index := range p.Rules {
		rule := &p.Rules[index]
		rule.Name = strings.TrimSpace(rule.Name)
		if rule.Name == "" || len(rule.Name) > 80 {
			return fmt.Errorf("rules[%d].name must contain between 1 and 80 characters", index)
		}
		key := strings.ToLower(rule.Name)
		if _, ok := seenNames[key]; ok {
			return fmt.Errorf("duplicate cache rule name %q", rule.Name)
		}
		seenNames[key] = struct{}{}
		if err := normalizeCacheTTL(&rule.TTL, fmt.Sprintf("rules[%d].ttl", index)); err != nil {
			return err
		}
		hasAll, err := normalizeCacheConditions(&rule.Conditions, fmt.Sprintf("rules[%d].conditions", index))
		if err != nil {
			return err
		}
		if hasAll && index != len(p.Rules)-1 {
			return fmt.Errorf("rules[%d] matches all requests and must be last", index)
		}
	}
	return nil
}

func normalizeCacheTTL(ttl *CacheTTL, prefix string) error {
	if ttl.DefaultSeconds < 1 || ttl.DefaultSeconds > 31536000 {
		return fmt.Errorf("%s.default_seconds must be between 1 and 31536000", prefix)
	}
	if ttl.OverrideClientTTL && (ttl.ClientSeconds < 0 || ttl.ClientSeconds > 31536000) {
		return fmt.Errorf("%s.client_seconds must be between 0 and 31536000", prefix)
	}
	for status, seconds := range ttl.Status {
		if !regexp.MustCompile(`^[1-5][0-9][0-9]$`).MatchString(status) || seconds < 1 || seconds > 31536000 {
			return fmt.Errorf("invalid %s.status TTL for %q", prefix, status)
		}
	}
	return nil
}

func normalizeCacheConditions(conditions *CacheConditions, prefix string) (bool, error) {
	conditions.GroupOperator = strings.ToUpper(strings.TrimSpace(conditions.GroupOperator))
	if conditions.GroupOperator == "" {
		conditions.GroupOperator = "OR"
	}
	if !booleanOperator(conditions.GroupOperator) {
		return false, fmt.Errorf("%s.group_operator must be AND or OR", prefix)
	}
	if len(conditions.Groups) == 0 || len(conditions.Groups) > 16 {
		return false, fmt.Errorf("%s must contain between 1 and 16 groups", prefix)
	}
	all := false
	for gi := range conditions.Groups {
		group := &conditions.Groups[gi]
		group.Operator = strings.ToUpper(strings.TrimSpace(group.Operator))
		if !booleanOperator(group.Operator) {
			return false, fmt.Errorf("%s.groups[%d].operator must be AND or OR", prefix, gi)
		}
		if len(group.Rules) == 0 || len(group.Rules) > 32 {
			return false, fmt.Errorf("%s.groups[%d] must contain between 1 and 32 rules", prefix, gi)
		}

		for ri := range group.Rules {
			rule := &group.Rules[ri]
			rule.Type = strings.ToUpper(strings.TrimSpace(rule.Type))
			switch rule.Type {
			case "ALL":
				all = true
				if len(group.Rules) != 1 || len(conditions.Groups) != 1 {
					return false, errors.New("ALL cannot be combined with other cache conditions")
				}
			case "EXTENSION":
				if len(rule.Values) == 0 {
					return false, fmt.Errorf("%s extension rule %d/%d requires values", prefix, gi, ri)
				}
				for i := range rule.Values {
					rule.Values[i] = strings.TrimPrefix(
						strings.ToLower(strings.TrimSpace(rule.Values[i])),
						".",
					)
					if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`).MatchString(rule.Values[i]) {
						return false, fmt.Errorf("invalid extension %q", rule.Values[i])
					}
				}
			case "PATH_PREFIX":
				if len(rule.Values) == 0 {
					return false, fmt.Errorf("%s path prefix rule %d/%d requires values", prefix, gi, ri)
				}
				for _, value := range rule.Values {
					if !strings.HasPrefix(value, "/") {
						return false, fmt.Errorf("path prefix %q must start with /", value)
					}
				}
			case "PATH_REGEX":
				rule.Value = strings.TrimSpace(rule.Value)
				if rule.Value == "" {
					return false, fmt.Errorf("%s path regex rule %d/%d requires value", prefix, gi, ri)
				}
				if _, err := regexp.Compile(rule.Value); err != nil {
					return false, fmt.Errorf("invalid path regex: %w", err)
				}
			default:
				return false, fmt.Errorf("unsupported cache condition type %q", rule.Type)
			}
		}
	}
	return all, nil
}

func booleanOperator(value string) bool { return value == "AND" || value == "OR" }
