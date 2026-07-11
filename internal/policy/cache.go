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
	Enabled            bool            `json:"enabled"`
	ResponseHeaders    CacheHeaders    `json:"response_headers"`
	AllowPurgeMethod   bool            `json:"allow_purge_method"`
	Stale              CacheStale      `json:"stale"`
	TTL                CacheTTL        `json:"ttl"`
	VaryHeaders        []string        `json:"vary_headers"`
	SurrogateKeyHeader string          `json:"surrogate_key_header"`
	Conditions         CacheConditions `json:"conditions"`
}

type CacheHeaders struct {
	XCache bool `json:"x_cache"`
	Age    bool `json:"age"`
}
type CacheStale struct {
	Enabled        bool `json:"enabled"`
	IfErrorSeconds int  `json:"if_error_seconds"`
}
type CacheTTL struct {
	DefaultSeconds int            `json:"default_seconds"`
	Status         map[string]int `json:"status"`
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
		Stale:              CacheStale{Enabled: true, IfErrorSeconds: 86400},
		TTL:                CacheTTL{DefaultSeconds: 300, Status: map[string]int{"200": 300, "301": 3600, "404": 60}},
		VaryHeaders:        []string{"Accept-Encoding"},
		SurrogateKeyHeader: "Surrogate-Key",
		Conditions:         CacheConditions{GroupOperator: "OR", Groups: []CacheConditionGroup{{Operator: "OR", Rules: []CacheConditionRule{{Type: "ALL"}}}}},
	}
}

func (p *CachePolicy) NormalizeAndValidate() error {
	p.Conditions.GroupOperator = strings.ToUpper(strings.TrimSpace(p.Conditions.GroupOperator))
	if p.Conditions.GroupOperator == "" {
		p.Conditions.GroupOperator = "OR"
	}
	if !booleanOperator(p.Conditions.GroupOperator) {
		return errors.New("conditions.group_operator must be AND or OR")
	}
	if p.TTL.DefaultSeconds < 1 || p.TTL.DefaultSeconds > 31536000 {
		return errors.New("ttl.default_seconds must be between 1 and 31536000")
	}
	if p.Stale.Enabled && (p.Stale.IfErrorSeconds < 1 || p.Stale.IfErrorSeconds > 31536000) {
		return errors.New("stale.if_error_seconds must be between 1 and 31536000")
	}
	for status, ttl := range p.TTL.Status {
		if !regexp.MustCompile(`^[1-5][0-9][0-9]$`).MatchString(status) || ttl < 1 || ttl > 31536000 {
			return fmt.Errorf("invalid TTL for status %q", status)
		}
	}
	seenHeaders := map[string]struct{}{}
	for i, header := range p.VaryHeaders {
		header = http.CanonicalHeaderKey(strings.TrimSpace(header))
		if header == "" {
			return errors.New("vary_headers cannot contain an empty header")
		}
		if _, ok := seenHeaders[header]; ok {
			return fmt.Errorf("duplicate vary header %q", header)
		}
		seenHeaders[header] = struct{}{}
		p.VaryHeaders[i] = header
	}
	sort.Strings(p.VaryHeaders)
	p.SurrogateKeyHeader = http.CanonicalHeaderKey(strings.TrimSpace(p.SurrogateKeyHeader))
	if p.SurrogateKeyHeader == "" {
		p.SurrogateKeyHeader = "Surrogate-Key"
	}
	if p.SurrogateKeyHeader != "Surrogate-Key" {
		return errors.New("surrogate_key_header must be Surrogate-Key")
	}
	if len(p.Conditions.Groups) == 0 || len(p.Conditions.Groups) > 16 {
		return errors.New("conditions must contain between 1 and 16 groups")
	}
	all := false
	for gi := range p.Conditions.Groups {
		group := &p.Conditions.Groups[gi]
		group.Operator = strings.ToUpper(strings.TrimSpace(group.Operator))
		if !booleanOperator(group.Operator) {
			return fmt.Errorf("conditions.groups[%d].operator must be AND or OR", gi)
		}
		if len(group.Rules) == 0 || len(group.Rules) > 32 {
			return fmt.Errorf("conditions.groups[%d] must contain between 1 and 32 rules", gi)
		}
		for ri := range group.Rules {
			rule := &group.Rules[ri]
			rule.Type = strings.ToUpper(strings.TrimSpace(rule.Type))
			switch rule.Type {
			case "ALL":
				all = true
				if len(group.Rules) != 1 || len(p.Conditions.Groups) != 1 {
					return errors.New("ALL cannot be combined with other cache conditions")
				}
			case "EXTENSION":
				if len(rule.Values) == 0 {
					return fmt.Errorf("extension rule %d/%d requires values", gi, ri)
				}
				for i := range rule.Values {
					rule.Values[i] = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(rule.Values[i])), ".")
					if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`).MatchString(rule.Values[i]) {
						return fmt.Errorf("invalid extension %q", rule.Values[i])
					}
				}
			case "PATH_PREFIX":
				if len(rule.Values) == 0 {
					return fmt.Errorf("path prefix rule %d/%d requires values", gi, ri)
				}
				for _, value := range rule.Values {
					if !strings.HasPrefix(value, "/") {
						return fmt.Errorf("path prefix %q must start with /", value)
					}
				}
			case "PATH_REGEX":
				rule.Value = strings.TrimSpace(rule.Value)
				if rule.Value == "" {
					return fmt.Errorf("path regex rule %d/%d requires value", gi, ri)
				}
				if _, err := regexp.Compile(rule.Value); err != nil {
					return fmt.Errorf("invalid path regex: %w", err)
				}
			default:
				return fmt.Errorf("unsupported cache condition type %q", rule.Type)
			}
		}
	}
	_ = all
	return nil
}

func booleanOperator(value string) bool { return value == "AND" || value == "OR" }
