package policy

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

const (
	WAFRuleTypeMatch     = "MATCH"
	WAFRuleTypeRateLimit = "RATE_LIMIT"

	WAFActionShowPage = "SHOW_PAGE"
	WAFActionBlock    = "BLOCK"
	WAFActionCaptcha  = "CAPTCHA"
	WAFActionRedirect = "REDIRECT"
	WAFActionAllow    = "ALLOW"
	WAFActionTag      = "TAG"
	WAFActionMonitor  = "MONITOR"

	WAFResponseDefault = "DEFAULT"
	WAFResponseHTML    = "HTML"
	WAFResponseText    = "TEXT"
	WAFResponseJSON    = "JSON"
)

const (
	defaultXSSPattern                = `(?i)(?:<\s*script\b|javascript\s*:|on(?:error|load|click|mouseover)\s*=|<\s*(?:iframe|object|embed|svg)\b)`
	defaultPathTraversalPattern      = `((\.+)(/+)){2,}`
	defaultSensitiveDirectoryPattern = `(?i)(?:^|/)\.(?:git|svn|htaccess|idea|env|vscode)(?:/|$)`
	defaultSQLInjectionPattern       = `(?i)(?:\bunion\b.{0,24}\bselect\b|\bselect\b.{0,24}\bfrom\b|\bor\b\s+['"]?\d+['"]?\s*=\s*['"]?\d+|(?:--|#|/\*)\s*$|\bsleep\s*\(|\bbenchmark\s*\()`
)

type WAFPolicy struct {
	Enabled  bool         `json:"enabled"`
	RuleSets []WAFRuleSet `json:"rule_sets"`
}

type WAFRuleSet struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Enabled bool      `json:"enabled"`
	Rules   []WAFRule `json:"rules"`
}

type WAFRule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Type    string `json:"type"`

	Conditions WAFConditions `json:"conditions"`

	Key           string `json:"key,omitempty"`
	KeyName       string `json:"key_name,omitempty"`
	Requests      int    `json:"requests,omitempty"`
	WindowSeconds int    `json:"window_seconds,omitempty"`
	Burst         int    `json:"burst,omitempty"`
	Backend       string `json:"backend,omitempty"`
	FailureMode   string `json:"failure_mode,omitempty"`

	Action WAFAction `json:"action"`
}

type WAFConditions struct {
	Operator string              `json:"operator"`
	Groups   []WAFConditionGroup `json:"groups"`
}

type WAFConditionGroup struct {
	ID         string         `json:"id"`
	Operator   string         `json:"operator"`
	Conditions []WAFCondition `json:"conditions"`
}

type WAFCondition struct {
	ID            string   `json:"id"`
	Field         string   `json:"field"`
	FieldName     string   `json:"field_name,omitempty"`
	Operator      string   `json:"operator"`
	Value         string   `json:"value,omitempty"`
	Values        []string `json:"values,omitempty"`
	Negate        bool     `json:"negate,omitempty"`
	CaseSensitive bool     `json:"case_sensitive,omitempty"`
}

type WAFAction struct {
	Type           string      `json:"type"`
	StatusCode     int         `json:"status_code,omitempty"`
	Response       WAFResponse `json:"response,omitempty"`
	RedirectURL    string      `json:"redirect_url,omitempty"`
	RedirectStatus int         `json:"redirect_status,omitempty"`
	Tag            string      `json:"tag,omitempty"`
}

type WAFResponse struct {
	Type string `json:"type"`
	Body string `json:"body,omitempty"`
}

func DefaultWAFPolicy() WAFPolicy {
	block := func(status int) WAFAction {
		return WAFAction{Type: WAFActionShowPage, StatusCode: status, Response: WAFResponse{Type: WAFResponseDefault}}
	}
	match := func(id, name, field, pattern string, action WAFAction) WAFRule {
		return WAFRule{
			ID: id, Name: name, Enabled: true, Type: WAFRuleTypeMatch,
			Conditions: singleWAFCondition(WAFCondition{Field: field, Operator: "REGEX", Value: pattern, CaseSensitive: true}),
			Action:     action,
		}
	}
	return WAFPolicy{
		Enabled: true,
		RuleSets: []WAFRuleSet{
			{
				ID: "builtin-xss", Name: "Cross-site scripting", Enabled: true,
				Rules: []WAFRule{
					match("builtin-xss-query", "XSS in query values", "QUERY_VALUES", defaultXSSPattern, block(http.StatusForbidden)),
					match("builtin-xss-body", "XSS in request body", "BODY", defaultXSSPattern, block(http.StatusForbidden)),
				},
			},
			{
				ID: "builtin-path-traversal", Name: "Path traversal", Enabled: true,
				Rules: []WAFRule{
					match("builtin-path-traversal-target", "Repeated parent traversal", "REQUEST_TARGET", defaultPathTraversalPattern, block(http.StatusForbidden)),
				},
			},
			{
				ID: "builtin-sensitive-directories", Name: "Sensitive directories", Enabled: true,
				Rules: []WAFRule{
					match("builtin-sensitive-directories-path", "Repository and environment files", "PATH", defaultSensitiveDirectoryPattern, block(http.StatusForbidden)),
				},
			},
			{
				ID: "builtin-sql-injection", Name: "SQL injection", Enabled: true,
				Rules: []WAFRule{
					match("builtin-sql-query", "SQL injection in query values", "QUERY_VALUES", defaultSQLInjectionPattern, block(http.StatusForbidden)),
					match("builtin-sql-body", "SQL injection in request body", "BODY", defaultSQLInjectionPattern, block(http.StatusForbidden)),
				},
			},
			{
				ID: "builtin-cc", Name: "CC attack", Enabled: true,
				Rules: []WAFRule{{
					ID: "builtin-cc-ip-path", Name: "Requests per client and path", Enabled: true,
					Type: WAFRuleTypeRateLimit, Key: "CLIENT_IP_PATH", Requests: 60, WindowSeconds: 60, Burst: 20,
					Backend: "REDIS", FailureMode: "LOCAL",
					Conditions: WAFConditions{Operator: "AND", Groups: []WAFConditionGroup{}},
					Action:     block(http.StatusTooManyRequests),
				}},
			},
		},
	}
}

func singleWAFCondition(condition WAFCondition) WAFConditions {
	return WAFConditions{Operator: "AND", Groups: []WAFConditionGroup{{Operator: "AND", Conditions: []WAFCondition{condition}}}}
}

func (p *WAFPolicy) NormalizeAndValidate() error {
	if p.RuleSets == nil {
		p.RuleSets = DefaultWAFPolicy().RuleSets
	}
	if len(p.RuleSets) > 64 {
		return errors.New("WAF policy cannot contain more than 64 rule sets")
	}
	seenIDs := map[string]struct{}{}
	for setIndex := range p.RuleSets {
		set := &p.RuleSets[setIndex]
		set.ID = strings.TrimSpace(set.ID)
		if set.ID == "" {
			set.ID = fmt.Sprintf("rule-set-%d", setIndex+1)
		}
		if err := uniqueWAFID(seenIDs, set.ID); err != nil {
			return fmt.Errorf("rule_sets[%d]: %w", setIndex, err)
		}
		set.Name = strings.TrimSpace(set.Name)
		if set.Name == "" {
			set.Name = fmt.Sprintf("Rule set %d", setIndex+1)
		}
		if len(set.Name) > 128 {
			return fmt.Errorf("rule_sets[%d].name cannot exceed 128 characters", setIndex)
		}
		if set.Rules == nil {
			set.Rules = []WAFRule{}
		}
		if len(set.Rules) > 64 {
			return fmt.Errorf("rule_sets[%d] cannot contain more than 64 rules", setIndex)
		}
		for ruleIndex := range set.Rules {
			location := fmt.Sprintf("rule_sets[%d].rules[%d]", setIndex, ruleIndex)
			rule := &set.Rules[ruleIndex]
			rule.ID = strings.TrimSpace(rule.ID)
			if rule.ID == "" {
				rule.ID = fmt.Sprintf("%s-rule-%d", set.ID, ruleIndex+1)
			}
			if err := uniqueWAFID(seenIDs, rule.ID); err != nil {
				return fmt.Errorf("%s: %w", location, err)
			}
			rule.Name = strings.TrimSpace(rule.Name)
			if rule.Name == "" {
				rule.Name = fmt.Sprintf("Rule %d", ruleIndex+1)
			}
			if len(rule.Name) > 128 {
				return fmt.Errorf("%s.name cannot exceed 128 characters", location)
			}
			rule.Type = strings.ToUpper(strings.TrimSpace(rule.Type))
			if rule.Type == "" {
				rule.Type = WAFRuleTypeMatch
			}
			switch rule.Type {
			case WAFRuleTypeMatch:
				rule.Key, rule.KeyName = "", ""
				rule.Requests, rule.WindowSeconds, rule.Burst = 0, 0, 0
				rule.Backend, rule.FailureMode = "", ""
				if err := normalizeWAFConditions(&rule.Conditions, location+".conditions", rule.ID, true, seenIDs); err != nil {
					return err
				}
			case WAFRuleTypeRateLimit:
				if err := normalizeRateRule(rule, location); err != nil {
					return err
				}
				if err := normalizeWAFConditions(&rule.Conditions, location+".conditions", rule.ID, false, seenIDs); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%s.type must be MATCH or RATE_LIMIT", location)
			}
			if err := normalizeWAFAction(&rule.Action, rule.Type, location+".action"); err != nil {
				return err
			}
		}
	}
	return nil
}

func uniqueWAFID(seen map[string]struct{}, id string) error {
	if len(id) > 128 {
		return errors.New("id cannot exceed 128 characters")
	}
	if _, exists := seen[id]; exists {
		return fmt.Errorf("duplicate id %q", id)
	}
	seen[id] = struct{}{}
	return nil
}

func normalizeWAFConditions(conditions *WAFConditions, location, idPrefix string, required bool, seenIDs map[string]struct{}) error {
	conditions.Operator = strings.ToUpper(strings.TrimSpace(conditions.Operator))
	if conditions.Operator == "" {
		conditions.Operator = "AND"
	}
	if conditions.Operator != "AND" && conditions.Operator != "OR" {
		return fmt.Errorf("%s.operator must be AND or OR", location)
	}
	if len(conditions.Groups) == 0 {
		if required {
			return fmt.Errorf("%s.groups must contain at least one group", location)
		}
		conditions.Groups = []WAFConditionGroup{}
		return nil
	}
	if len(conditions.Groups) > 16 {
		return fmt.Errorf("%s cannot contain more than 16 groups", location)
	}
	for groupIndex := range conditions.Groups {
		group := &conditions.Groups[groupIndex]
		groupLocation := fmt.Sprintf("%s.groups[%d]", location, groupIndex)
		group.ID = strings.TrimSpace(group.ID)
		if group.ID == "" {
			group.ID = generatedWAFChildID("group", idPrefix, groupIndex)
		}
		if err := uniqueWAFID(seenIDs, group.ID); err != nil {
			return fmt.Errorf("%s: %w", groupLocation, err)
		}
		group.Operator = strings.ToUpper(strings.TrimSpace(group.Operator))
		if group.Operator == "" {
			group.Operator = "AND"
		}
		if group.Operator != "AND" && group.Operator != "OR" {
			return fmt.Errorf("%s.operator must be AND or OR", groupLocation)
		}
		if len(group.Conditions) == 0 || len(group.Conditions) > 16 {
			return fmt.Errorf("%s.conditions must contain between 1 and 16 conditions", groupLocation)
		}
		for conditionIndex := range group.Conditions {
			conditionLocation := fmt.Sprintf("%s.conditions[%d]", groupLocation, conditionIndex)
			condition := &group.Conditions[conditionIndex]
			condition.ID = strings.TrimSpace(condition.ID)
			if condition.ID == "" {
				condition.ID = generatedWAFChildID("condition", group.ID, conditionIndex)
			}
			if err := uniqueWAFID(seenIDs, condition.ID); err != nil {
				return fmt.Errorf("%s: %w", conditionLocation, err)
			}
			if err := normalizeWAFCondition(condition, conditionLocation); err != nil {
				return err
			}
		}
	}
	return nil
}

func generatedWAFChildID(kind, parent string, index int) string {
	digest := sha256.Sum256([]byte(parent))
	return fmt.Sprintf("%s-%x-%d", kind, digest[:8], index+1)
}

func normalizeWAFCondition(condition *WAFCondition, location string) error {
	condition.Field = strings.ToUpper(strings.TrimSpace(condition.Field))
	condition.FieldName = strings.TrimSpace(condition.FieldName)
	condition.Operator = strings.ToUpper(strings.TrimSpace(condition.Operator))
	condition.Value = strings.TrimSpace(condition.Value)
	for index := range condition.Values {
		condition.Values[index] = strings.TrimSpace(condition.Values[index])
	}
	switch condition.Field {
	case "METHOD", "HOST", "PATH", "RAW_QUERY", "QUERY_VALUES", "REQUEST_TARGET", "BODY", "CLIENT_IP", "USER_AGENT":
		condition.FieldName = ""
	case "QUERY", "COOKIE":
		if condition.FieldName == "" {
			return fmt.Errorf("%s.field_name is required for %s", location, condition.Field)
		}
	case "HEADER":
		condition.FieldName = http.CanonicalHeaderKey(condition.FieldName)
		if condition.FieldName == "" {
			return fmt.Errorf("%s.field_name is required for HEADER", location)
		}
	default:
		return fmt.Errorf("%s.field %q is unsupported", location, condition.Field)
	}
	switch condition.Operator {
	case "EXISTS":
		condition.Value, condition.Values = "", nil
	case "EQUALS", "CONTAINS", "PREFIX", "SUFFIX", "REGEX":
		if condition.Value == "" {
			return fmt.Errorf("%s.value is required", location)
		}
		if condition.Operator == "REGEX" {
			pattern := condition.Value
			if !condition.CaseSensitive {
				pattern = "(?i)" + pattern
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("%s has invalid regex: %w", location, err)
			}
		}
	case "IN":
		if len(condition.Values) == 0 {
			return fmt.Errorf("%s.values is required for IN", location)
		}
	case "CIDR":
		if condition.Field != "CLIENT_IP" || len(condition.Values) == 0 {
			return fmt.Errorf("%s CIDR requires CLIENT_IP and at least one value", location)
		}
		for _, value := range condition.Values {
			if _, err := netip.ParsePrefix(value); err != nil {
				return fmt.Errorf("%s contains invalid CIDR %q", location, value)
			}
		}
	default:
		return fmt.Errorf("%s.operator %q is unsupported", location, condition.Operator)
	}
	return nil
}

func normalizeRateRule(rule *WAFRule, location string) error {
	rule.Key = strings.ToUpper(strings.TrimSpace(rule.Key))
	rule.KeyName = strings.TrimSpace(rule.KeyName)
	switch rule.Key {
	case "CLIENT_IP", "CLIENT_IP_PATH", "PATH", "GLOBAL":
		rule.KeyName = ""
	case "HEADER":
		rule.KeyName = http.CanonicalHeaderKey(rule.KeyName)
		if rule.KeyName == "" {
			return fmt.Errorf("%s.key_name is required for HEADER", location)
		}
	case "COOKIE":
		if rule.KeyName == "" {
			return fmt.Errorf("%s.key_name is required for COOKIE", location)
		}
	default:
		return fmt.Errorf("%s.key %q is unsupported", location, rule.Key)
	}
	if rule.Requests < 1 || rule.Requests > 1_000_000 {
		return fmt.Errorf("%s.requests must be between 1 and 1000000", location)
	}
	if rule.WindowSeconds < 1 || rule.WindowSeconds > 3600 {
		return fmt.Errorf("%s.window_seconds must be between 1 and 3600", location)
	}
	if rule.Burst < 0 || rule.Burst > rule.Requests*10 {
		return fmt.Errorf("%s.burst must be between 0 and requests*10", location)
	}
	rule.Backend = strings.ToUpper(strings.TrimSpace(rule.Backend))
	if rule.Backend == "" {
		rule.Backend = "LOCAL"
	}
	if rule.Backend != "LOCAL" && rule.Backend != "REDIS" {
		return fmt.Errorf("%s.backend must be LOCAL or REDIS", location)
	}
	rule.FailureMode = strings.ToUpper(strings.TrimSpace(rule.FailureMode))
	if rule.FailureMode == "" {
		rule.FailureMode = "LOCAL"
	}
	if rule.FailureMode != "OPEN" && rule.FailureMode != "CLOSED" && rule.FailureMode != "LOCAL" {
		return fmt.Errorf("%s.failure_mode must be OPEN, CLOSED or LOCAL", location)
	}
	if rule.Backend == "LOCAL" {
		rule.FailureMode = "LOCAL"
	}
	return nil
}

func normalizeWAFAction(action *WAFAction, ruleType, location string) error {
	action.Type = strings.ToUpper(strings.TrimSpace(action.Type))
	if action.Type == "" {
		action.Type = WAFActionShowPage
	}
	switch action.Type {
	case WAFActionShowPage, WAFActionBlock:
		if action.StatusCode == 0 {
			if ruleType == WAFRuleTypeRateLimit {
				action.StatusCode = http.StatusTooManyRequests
			} else {
				action.StatusCode = http.StatusForbidden
			}
		}
		if action.StatusCode < 400 || action.StatusCode > 599 {
			return fmt.Errorf("%s.status_code must be between 400 and 599", location)
		}
		if action.Type == WAFActionShowPage {
			if err := normalizeWAFResponse(&action.Response, location+".response"); err != nil {
				return err
			}
		}
	case WAFActionCaptcha, WAFActionAllow, WAFActionMonitor:
	case WAFActionRedirect:
		action.RedirectURL = strings.TrimSpace(action.RedirectURL)
		if strings.ContainsAny(action.RedirectURL, "\r\n") || action.RedirectURL == "" {
			return fmt.Errorf("%s.redirect_url is invalid", location)
		}
		parsed, err := url.Parse(action.RedirectURL)
		if err != nil || (parsed.IsAbs() && parsed.Scheme != "http" && parsed.Scheme != "https") || (!parsed.IsAbs() && !strings.HasPrefix(action.RedirectURL, "/")) {
			return fmt.Errorf("%s.redirect_url must be an HTTP(S) URL or absolute path", location)
		}
		if action.RedirectStatus == 0 {
			action.RedirectStatus = http.StatusFound
		}
		switch action.RedirectStatus {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		default:
			return fmt.Errorf("%s.redirect_status is unsupported", location)
		}
	case WAFActionTag:
		action.Tag = strings.TrimSpace(action.Tag)
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`).MatchString(action.Tag) {
			return fmt.Errorf("%s.tag must be 1-64 safe characters", location)
		}
	default:
		return fmt.Errorf("%s.type %q is unsupported", location, action.Type)
	}
	return nil
}

func normalizeWAFResponse(response *WAFResponse, location string) error {
	response.Type = strings.ToUpper(strings.TrimSpace(response.Type))
	if response.Type == "" {
		response.Type = WAFResponseDefault
	}
	if len(response.Body) > 128<<10 {
		return fmt.Errorf("%s.body cannot exceed 131072 bytes", location)
	}
	switch response.Type {
	case WAFResponseDefault:
		response.Body = ""
	case WAFResponseHTML, WAFResponseText:
		if response.Body == "" {
			return fmt.Errorf("%s.body is required for %s", location, response.Type)
		}
	case WAFResponseJSON:
		if !json.Valid([]byte(response.Body)) {
			return fmt.Errorf("%s.body must contain valid JSON", location)
		}
	default:
		return fmt.Errorf("%s.type must be DEFAULT, HTML, TEXT or JSON", location)
	}
	return nil
}
