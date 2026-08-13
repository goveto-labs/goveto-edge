// Package wafdsl translates the operator-facing WAF DSL to the canonical policy model.
package wafdsl

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/alecthomas/participle/v2/lexer"
	"github.com/google/uuid"

	"goveto-edge/internal/policy"
)

type Scope string

const (
	ScopePolicy  Scope = "POLICY"
	ScopeRuleSet Scope = "RULE_SET"
	ScopeRule    Scope = "RULE"
	ScopeGroup   Scope = "GROUP"
)

type Target struct {
	RuleSetID string `json:"rule_set_id,omitempty"`
	RuleID    string `json:"rule_id,omitempty"`
	GroupID   string `json:"group_id,omitempty"`
}

type Diagnostic struct {
	Severity  string `json:"severity"`
	Message   string `json:"message"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"end_line"`
	EndColumn int    `json:"end_column"`
}

type Result struct {
	Valid       bool              `json:"valid"`
	Source      string            `json:"source"`
	Policy      *policy.WAFPolicy `json:"policy,omitempty"`
	Diagnostics []Diagnostic      `json:"diagnostics"`
}

var dslLexer = lexer.MustSimple([]lexer.SimpleRule{
	{Name: "Whitespace", Pattern: `[ \t\r\n]+`},
	{Name: "Comment", Pattern: `(?m)(//[^\n]*|#[^\n]*)`},
	{Name: "CIDR", Pattern: `[0-9A-Fa-f:.]+/[0-9]+`},
	{Name: "Address", Pattern: `(?:[0-9]+(?:\.[0-9]+){3}|[0-9A-Fa-f]*:[0-9A-Fa-f:.]+)`},
	{Name: "Duration", Pattern: `[0-9]+s`},
	{Name: "Number", Pattern: `[0-9]+`},
	{Name: "String", Pattern: `"(?:\\.|[^"\\])*"`},
	{Name: "Ident", Pattern: `[A-Za-z_][A-Za-z0-9_.-]*`},
	{Name: "Punct", Pattern: `[][{}()+,]`},
})

type token struct {
	typeName string
	value    string
	line     int
	column   int
}

type parser struct {
	tokens []token
	index  int
}

type parseError struct {
	message string
	line    int
	column  int
}

func (e *parseError) Error() string { return e.message }

func Parse(source string, scope Scope) (any, []Diagnostic) {
	p, err := newParser(source)
	if err != nil {
		return nil, diagnosticsFor(err)
	}
	var value any
	switch scope {
	case ScopePolicy:
		value, err = p.parsePolicy()
	case ScopeRuleSet:
		value, err = p.parseRuleSet()
	case ScopeRule:
		value, err = p.parseRule()
	case ScopeGroup:
		value, err = p.parseGroupFragment()
	default:
		err = &parseError{message: fmt.Sprintf("unsupported DSL scope %q", scope), line: 1, column: 1}
	}
	if err == nil && !p.done() {
		t := p.peek()
		err = &parseError{message: fmt.Sprintf("unexpected token %q", t.value), line: t.line, column: t.column}
	}
	if err != nil {
		return nil, diagnosticsFor(err)
	}
	return value, nil
}

func newParser(source string) (*parser, error) {
	stream, err := dslLexer.Lex("waf.dsl", strings.NewReader(source))
	if err != nil {
		return nil, err
	}
	symbols := dslLexer.Symbols()
	names := make(map[lexer.TokenType]string, len(symbols))
	for name, symbol := range symbols {
		names[symbol] = name
	}
	result := &parser{}
	for {
		next, nextErr := stream.Next()
		if nextErr != nil {
			return nil, nextErr
		}
		if next.EOF() {
			break
		}
		name := names[next.Type]
		if name == "Whitespace" || name == "Comment" {
			continue
		}
		result.tokens = append(result.tokens, token{typeName: name, value: next.Value, line: next.Pos.Line, column: next.Pos.Column})
	}
	return result, nil
}

func diagnosticsFor(err error) []Diagnostic {
	line, column := 1, 1
	message := err.Error()
	if positioned, ok := err.(*parseError); ok {
		line, column, message = positioned.line, positioned.column, positioned.message
	} else if positioned, ok := err.(interface {
		Message() string
		Position() lexer.Position
	}); ok {
		message = positioned.Message()
		line, column = positioned.Position().Line, positioned.Position().Column
	}
	return []Diagnostic{{Severity: "error", Message: message, Line: line, Column: column, EndLine: line, EndColumn: column + 1}}
}

func (p *parser) parsePolicy() (policy.WAFPolicy, error) {
	if err := p.expect("waf"); err != nil {
		return policy.WAFPolicy{}, err
	}
	enabled, err := p.parseEnabled()
	if err != nil {
		return policy.WAFPolicy{}, err
	}
	if err = p.expect("{"); err != nil {
		return policy.WAFPolicy{}, err
	}
	result := policy.WAFPolicy{Enabled: enabled, TrustedProxies: []string{}, RuleSets: []policy.WAFRuleSet{}}
	seenProxyChain, seenProxies := false, false
	seenBodyLimit, seenBodyOverLimit := false, false
	for !p.accept("}") {
		if p.done() {
			return result, p.errorHere("expected policy property or closing brace")
		}
		switch p.peek().value {
		case "trusted_proxy_chain":
			if seenProxyChain {
				return result, p.errorHere("trusted_proxy_chain may only be declared once")
			}
			p.index++
			result.TrustedProxyChain, err = p.parseEnabled()
			seenProxyChain = true
		case "trusted_proxies":
			if seenProxies {
				return result, p.errorHere("trusted_proxies may only be declared once")
			}
			p.index++
			result.TrustedProxies, err = p.parseStringSet(true)
			seenProxies = true
		case "body_inspect_limit":
			if seenBodyLimit {
				return result, p.errorHere("body_inspect_limit may only be declared once")
			}
			p.index++
			var limit int
			limit, err = p.parseNumber()
			result.BodyInspectLimitBytes = int64(limit)
			seenBodyLimit = true
		case "body_over_limit":
			if seenBodyOverLimit {
				return result, p.errorHere("body_over_limit may only be declared once")
			}
			p.index++
			if p.accept("block") {
				result.BodyOverLimitAction = policy.WAFBodyOverLimitBlock
			} else if p.accept("partial") {
				result.BodyOverLimitAction = policy.WAFBodyOverLimitPartial
			} else {
				err = p.errorHere("expected block or partial")
			}
			seenBodyOverLimit = true
		case "ruleset":
			var set policy.WAFRuleSet
			set, err = p.parseRuleSet()
			result.RuleSets = append(result.RuleSets, set)
		default:
			err = p.errorHere("expected trusted_proxy_chain, trusted_proxies, body_inspect_limit, body_over_limit, or ruleset")
		}
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func (p *parser) parseRuleSet() (policy.WAFRuleSet, error) {
	result := policy.WAFRuleSet{Rules: []policy.WAFRule{}}
	if err := p.expect("ruleset"); err != nil {
		return result, err
	}
	var err error
	if result.ID, err = p.parseString(); err != nil {
		return result, err
	}
	if result.Name, err = p.parseString(); err != nil {
		return result, err
	}
	if result.Enabled, err = p.parseEnabled(); err != nil {
		return result, err
	}
	if err = p.expect("{"); err != nil {
		return result, err
	}
	for !p.accept("}") {
		if p.done() {
			return result, p.errorHere("expected rule or closing brace")
		}
		rule, parseErr := p.parseRule()
		if parseErr != nil {
			return result, parseErr
		}
		result.Rules = append(result.Rules, rule)
	}
	return result, nil
}

func (p *parser) parseRule() (policy.WAFRule, error) {
	result := policy.WAFRule{Conditions: policy.WAFConditions{Operator: "AND", Groups: []policy.WAFConditionGroup{}}, Action: policy.WAFAction{}}
	kind := p.peek().value
	switch kind {
	case "rule":
		result.Type = policy.WAFRuleTypeMatch
	case "rate_limit":
		result.Type = policy.WAFRuleTypeRateLimit
	default:
		return result, p.errorHere("expected rule or rate_limit")
	}
	p.index++
	var err error
	if result.ID, err = p.parseString(); err != nil {
		return result, err
	}
	if result.Name, err = p.parseString(); err != nil {
		return result, err
	}
	if result.Enabled, err = p.parseEnabled(); err != nil {
		return result, err
	}
	if err = p.expect("{"); err != nil {
		return result, err
	}
	seenWhen, seenAction := false, false
	seenCount, seenLimit, seenBackend := false, false, false
	for !p.accept("}") {
		if p.done() {
			return result, p.errorHere("expected rule property or closing brace")
		}
		switch p.peek().value {
		case "when":
			if seenWhen {
				return result, p.errorHere("when may only be declared once")
			}
			p.index++
			result.Conditions, err = p.parseConditions()
			seenWhen = true
		case "count_by":
			if result.Type != policy.WAFRuleTypeRateLimit || seenCount {
				return result, p.errorHere("count_by is only valid once in a rate_limit rule")
			}
			p.index++
			result.Key, result.KeyName, err = p.parseRateKey()
			seenCount = true
		case "limit":
			if result.Type != policy.WAFRuleTypeRateLimit || seenLimit {
				return result, p.errorHere("limit is only valid once in a rate_limit rule")
			}
			p.index++
			result.Requests, err = p.parseNumber()
			if err == nil {
				err = p.expect("per")
			}
			if err == nil {
				result.WindowSeconds, err = p.parseDuration()
			}
			if err == nil {
				err = p.expect("burst")
			}
			if err == nil {
				result.Burst, err = p.parseNumber()
			}
			seenLimit = true
		case "backend":
			if result.Type != policy.WAFRuleTypeRateLimit || seenBackend {
				return result, p.errorHere("backend is only valid once in a rate_limit rule")
			}
			p.index++
			result.Backend, result.FailureMode, err = p.parseBackend()
			seenBackend = true
		case "then":
			if seenAction {
				return result, p.errorHere("then may only be declared once")
			}
			p.index++
			result.Action, err = p.parseAction()
			seenAction = true
		default:
			err = p.errorHere("expected when, count_by, limit, backend, or then")
		}
		if err != nil {
			return result, err
		}
	}
	if result.Type == policy.WAFRuleTypeMatch && !seenWhen {
		return result, p.errorHere("rule requires a when expression")
	}
	if result.Type == policy.WAFRuleTypeRateLimit && (!seenCount || !seenLimit || !seenBackend) {
		return result, p.errorHere("rate_limit requires count_by, limit, and backend")
	}
	if !seenAction {
		return result, p.errorHere("rule requires a then action")
	}
	return result, nil
}

func (p *parser) parseConditions() (policy.WAFConditions, error) {
	result := policy.WAFConditions{Operator: "AND", Groups: []policy.WAFConditionGroup{}}
	if err := p.expect("("); err != nil {
		return result, err
	}
	for {
		if err := p.expect("("); err != nil {
			return result, p.wrapError(err, "each condition group must be parenthesized")
		}
		group, err := p.parseGroupBody()
		if err != nil {
			return result, err
		}
		if err = p.expect(")"); err != nil {
			return result, err
		}
		result.Groups = append(result.Groups, group)
		if p.accept(")") {
			break
		}
		connector, err := p.parseConnector()
		if err != nil {
			return result, err
		}
		if len(result.Groups) == 1 {
			result.Operator = connector
		} else if result.Operator != connector {
			return result, p.errorHere("outer condition groups must use one consistent and/or operator")
		}
	}
	return result, nil
}

func (p *parser) parseGroupFragment() (policy.WAFConditionGroup, error) {
	if err := p.expect("group"); err != nil {
		return policy.WAFConditionGroup{}, err
	}
	if err := p.expect("("); err != nil {
		return policy.WAFConditionGroup{}, err
	}
	group, err := p.parseGroupBody()
	if err != nil {
		return group, err
	}
	if err = p.expect(")"); err != nil {
		return group, err
	}
	return group, nil
}

func (p *parser) parseGroupBody() (policy.WAFConditionGroup, error) {
	result := policy.WAFConditionGroup{Operator: "AND", Conditions: []policy.WAFCondition{}}
	if p.peek().value == ")" {
		return result, nil
	}
	for {
		condition, err := p.parseCondition()
		if err != nil {
			return result, err
		}
		result.Conditions = append(result.Conditions, condition)
		if p.done() || p.peek().value == ")" {
			break
		}
		connector, err := p.parseConnector()
		if err != nil {
			return result, err
		}
		if len(result.Conditions) == 1 {
			result.Operator = connector
		} else if result.Operator != connector {
			return result, p.errorHere("conditions in a group must use one consistent and/or operator")
		}
	}
	return result, nil
}

func (p *parser) parseCondition() (policy.WAFCondition, error) {
	result := policy.WAFCondition{}
	if p.accept("not") {
		result.Negate = true
	}
	fieldToken := p.peek()
	if fieldToken.typeName != "Ident" {
		return result, p.errorHere("expected a request field")
	}
	p.index++
	fieldValue := fieldToken.value
	if (fieldValue == "http.request.uri.query" || fieldValue == "http.request.headers" || fieldValue == "http.request.cookies") && p.accept("[") {
		name, err := p.parseString()
		if err != nil {
			return result, err
		}
		if err = p.expect("]"); err != nil {
			return result, err
		}
		fieldValue += "[" + strconv.Quote(name) + "]"
	}
	field, name, ok := parseField(fieldValue)
	if !ok {
		return result, &parseError{message: fmt.Sprintf("unsupported request field %q", fieldValue), line: fieldToken.line, column: fieldToken.column}
	}
	result.Field, result.FieldName = field, name
	operator := p.peek().value
	p.index++
	switch operator {
	case "exists":
		result.Operator = "EXISTS"
	case "eq", "contains", "starts_with", "ends_with", "matches":
		mapping := map[string]string{"eq": "EQUALS", "contains": "CONTAINS", "starts_with": "PREFIX", "ends_with": "SUFFIX", "matches": "REGEX"}
		result.Operator = mapping[operator]
		value, err := p.parseString()
		if err != nil {
			return result, err
		}
		result.Value = value
	case "in":
		values, cidr, err := p.parseConditionSet(result.Field == "CLIENT_IP")
		if err != nil {
			return result, err
		}
		if cidr {
			for index, value := range values {
				if strings.Contains(value, "/") {
					continue
				}
				address, parseErr := netip.ParseAddr(value)
				if parseErr != nil {
					return result, p.errorHere(fmt.Sprintf("invalid IP address %q", value))
				}
				values[index] = netip.PrefixFrom(address, address.BitLen()).String()
			}
		}
		result.Values = values
		if cidr {
			result.Operator = "CIDR"
		} else {
			result.Operator = "IN"
		}
	default:
		return result, &parseError{message: fmt.Sprintf("unsupported condition operator %q", operator), line: p.previous().line, column: p.previous().column}
	}
	if p.accept("case_sensitive") {
		result.CaseSensitive = true
	}
	return result, nil
}

func (p *parser) parseAction() (policy.WAFAction, error) {
	result := policy.WAFAction{}
	action := p.peek()
	if action.typeName != "Ident" {
		return result, p.errorHere("expected an action")
	}
	p.index++
	switch action.value {
	case "block", "show_page":
		if action.value == "block" {
			result.Type = policy.WAFActionBlock
		} else {
			result.Type = policy.WAFActionShowPage
		}
		if err := p.expect("status"); err != nil {
			return result, err
		}
		status, err := p.parseNumber()
		if err != nil {
			return result, err
		}
		result.StatusCode = status
		if result.Type == policy.WAFActionShowPage {
			if err = p.expect("response"); err != nil {
				return result, err
			}
			responseType := p.peek().value
			p.index++
			result.Response.Type = strings.ToUpper(responseType)
			if responseType != "default" {
				result.Response.Body, err = p.parseString()
				if err != nil {
					return result, err
				}
			}
		}
	case "captcha":
		result.Type = policy.WAFActionCaptcha
	case "allow":
		result.Type = policy.WAFActionAllow
	case "monitor":
		result.Type = policy.WAFActionMonitor
	case "redirect":
		result.Type = policy.WAFActionRedirect
		var err error
		if result.RedirectURL, err = p.parseString(); err != nil {
			return result, err
		}
		if err = p.expect("status"); err != nil {
			return result, err
		}
		if result.RedirectStatus, err = p.parseNumber(); err != nil {
			return result, err
		}
	case "tag":
		result.Type = policy.WAFActionTag
		var err error
		if result.Tag, err = p.parseString(); err != nil {
			return result, err
		}
	default:
		return result, &parseError{message: fmt.Sprintf("unsupported action %q", action.value), line: action.line, column: action.column}
	}
	return result, nil
}

func (p *parser) parseRateKey() (string, string, error) {
	first := p.peek()
	if first.typeName != "Ident" {
		return "", "", p.errorHere("expected a counter key")
	}
	p.index++
	fieldValue := first.value
	if (fieldValue == "http.request.headers" || fieldValue == "http.request.cookies") && p.accept("[") {
		name, err := p.parseString()
		if err != nil {
			return "", "", err
		}
		if err = p.expect("]"); err != nil {
			return "", "", err
		}
		fieldValue += "[" + strconv.Quote(name) + "]"
	}
	if fieldValue == "ip.src" && p.accept("+") {
		if err := p.expect("http.request.uri.path"); err != nil {
			return "", "", err
		}
		return "CLIENT_IP_PATH", "", nil
	}
	switch fieldValue {
	case "ip.src":
		return "CLIENT_IP", "", nil
	case "http.request.uri.path":
		return "PATH", "", nil
	case "global":
		return "GLOBAL", "", nil
	}
	field, name, ok := parseField(fieldValue)
	if ok && (field == "HEADER" || field == "COOKIE") {
		return field, name, nil
	}
	return "", "", &parseError{message: fmt.Sprintf("unsupported counter key %q", fieldValue), line: first.line, column: first.column}
}

func (p *parser) parseBackend() (string, string, error) {
	backend := strings.ToUpper(p.peek().value)
	if backend != "LOCAL" && backend != "REDIS" {
		return "", "", p.errorHere("backend must be local or redis")
	}
	p.index++
	if backend == "LOCAL" {
		return backend, "LOCAL", nil
	}
	if err := p.expect("fallback"); err != nil {
		return "", "", err
	}
	fallback := strings.ToUpper(p.peek().value)
	if fallback != "LOCAL" && fallback != "OPEN" && fallback != "CLOSED" {
		return "", "", p.errorHere("fallback must be local, open, or closed")
	}
	p.index++
	return backend, fallback, nil
}

func parseField(value string) (string, string, bool) {
	fields := map[string]string{
		"http.request.method": "METHOD", "http.host": "HOST", "http.request.uri.path": "PATH",
		"http.request.uri.query": "RAW_QUERY", "http.request.uri.query.values": "QUERY_VALUES",
		"http.request.target": "REQUEST_TARGET", "http.request.body.raw": "BODY", "ip.src": "CLIENT_IP",
		"ip.src.country": "COUNTRY", "ip.src.region_code": "REGION", "http.user_agent": "USER_AGENT",
	}
	if field, ok := fields[value]; ok {
		return field, "", true
	}
	for prefix, field := range map[string]string{
		"http.request.uri.query[": "QUERY", "http.request.headers[": "HEADER", "http.request.cookies[": "COOKIE",
	} {
		if strings.HasPrefix(value, prefix) && strings.HasSuffix(value, "]") {
			raw := value[len(prefix) : len(value)-1]
			name, err := strconv.Unquote(raw)
			if err == nil && name != "" {
				return field, name, true
			}
		}
	}
	return "", "", false
}

func fieldName(condition policy.WAFCondition) string {
	fields := map[string]string{
		"METHOD": "http.request.method", "HOST": "http.host", "PATH": "http.request.uri.path",
		"RAW_QUERY": "http.request.uri.query", "QUERY_VALUES": "http.request.uri.query.values",
		"REQUEST_TARGET": "http.request.target", "BODY": "http.request.body.raw", "CLIENT_IP": "ip.src",
		"COUNTRY": "ip.src.country", "REGION": "ip.src.region_code", "USER_AGENT": "http.user_agent",
	}
	if value, ok := fields[condition.Field]; ok {
		return value
	}
	prefixes := map[string]string{"QUERY": "http.request.uri.query", "HEADER": "http.request.headers", "COOKIE": "http.request.cookies"}
	return prefixes[condition.Field] + "[" + strconv.Quote(condition.FieldName) + "]"
}

func (p *parser) parseConditionSet(allowCIDR bool) ([]string, bool, error) {
	if err := p.expect("{"); err != nil {
		return nil, false, err
	}
	values := []string{}
	cidr, stringsSeen := false, false
	for !p.accept("}") {
		t := p.peek()
		if allowCIDR && (t.typeName == "CIDR" || t.typeName == "Address") {
			if stringsSeen {
				return nil, false, p.errorHere("CIDR and string values cannot be mixed in one set")
			}
			cidr = true
			values = append(values, t.value)
			p.index++
		} else if t.typeName == "String" {
			if cidr {
				return nil, false, p.errorHere("CIDR and string values cannot be mixed in one set")
			}
			stringsSeen = true
			value, err := p.parseString()
			if err != nil {
				return nil, false, err
			}
			values = append(values, value)
		} else {
			return nil, false, p.errorHere("expected a quoted value or CIDR")
		}
		p.accept(",")
	}
	return values, cidr, nil
}

func (p *parser) parseStringSet(allowCIDR bool) ([]string, error) {
	values, _, err := p.parseConditionSet(allowCIDR)
	return values, err
}

func (p *parser) parseString() (string, error) {
	t := p.peek()
	if t.typeName != "String" {
		return "", p.errorHere("expected a quoted string")
	}
	p.index++
	value, err := strconv.Unquote(t.value)
	if err != nil {
		return "", &parseError{message: err.Error(), line: t.line, column: t.column}
	}
	return value, nil
}

func (p *parser) parseNumber() (int, error) {
	t := p.peek()
	if t.typeName != "Number" {
		return 0, p.errorHere("expected a number")
	}
	p.index++
	value, err := strconv.Atoi(t.value)
	if err != nil {
		return 0, &parseError{message: err.Error(), line: t.line, column: t.column}
	}
	return value, nil
}

func (p *parser) parseDuration() (int, error) {
	t := p.peek()
	if t.typeName != "Duration" {
		return 0, p.errorHere("expected a duration in seconds, such as 60s")
	}
	p.index++
	return strconv.Atoi(strings.TrimSuffix(t.value, "s"))
}

func (p *parser) parseEnabled() (bool, error) {
	if p.accept("enabled") {
		return true, nil
	}
	if p.accept("disabled") {
		return false, nil
	}
	return false, p.errorHere("expected enabled or disabled")
}

func (p *parser) parseConnector() (string, error) {
	if p.accept("and") {
		return "AND", nil
	}
	if p.accept("or") {
		return "OR", nil
	}
	return "", p.errorHere("expected and, or, or a closing parenthesis")
}

func (p *parser) expect(value string) error {
	if p.accept(value) {
		return nil
	}
	return p.errorHere(fmt.Sprintf("expected %q", value))
}

func (p *parser) accept(value string) bool {
	if !p.done() && p.tokens[p.index].value == value {
		p.index++
		return true
	}
	return false
}

func (p *parser) peek() token {
	if p.done() {
		if len(p.tokens) == 0 {
			return token{line: 1, column: 1}
		}
		last := p.tokens[len(p.tokens)-1]
		return token{line: last.line, column: last.column + len(last.value)}
	}
	return p.tokens[p.index]
}

func (p *parser) previous() token {
	if p.index == 0 {
		return p.peek()
	}
	return p.tokens[p.index-1]
}

func (p *parser) done() bool { return p.index >= len(p.tokens) }

func (p *parser) errorHere(message string) error {
	t := p.peek()
	return &parseError{message: message, line: t.line, column: t.column}
}

func (p *parser) wrapError(err error, message string) error {
	if positioned, ok := err.(*parseError); ok {
		return &parseError{message: message + ": " + positioned.message, line: positioned.line, column: positioned.column}
	}
	return err
}

func ParseAndApply(current policy.WAFPolicy, scope Scope, target Target, source string) Result {
	parsed, diagnostics := Parse(source, scope)
	if len(diagnostics) > 0 {
		return Result{Source: source, Diagnostics: diagnostics}
	}
	candidate := clonePolicy(current)
	if err := applyFragment(&candidate, current, scope, target, parsed); err != nil {
		return Result{Source: source, Diagnostics: diagnosticsFor(err)}
	}
	if err := candidate.NormalizeAndValidatePublic(); err != nil {
		return Result{Source: source, Diagnostics: []Diagnostic{{Severity: "error", Message: err.Error(), Line: 1, Column: 1, EndLine: 1, EndColumn: 2}}}
	}
	if scope == ScopeRuleSet {
		target.RuleSetID = parsed.(policy.WAFRuleSet).ID
	}
	if scope == ScopeRule {
		target.RuleID = parsed.(policy.WAFRule).ID
	}
	formatted, err := Render(candidate, scope, target)
	if err != nil {
		return Result{Source: source, Diagnostics: diagnosticsFor(err)}
	}
	return Result{Valid: true, Source: formatted, Policy: &candidate, Diagnostics: []Diagnostic{}}
}

func Render(current policy.WAFPolicy, scope Scope, target Target) (string, error) {
	switch scope {
	case ScopePolicy:
		return formatPolicy(current), nil
	case ScopeRuleSet:
		set, _, err := findRuleSet(current, target.RuleSetID)
		if err != nil {
			return "", err
		}
		return formatRuleSet(set, 0), nil
	case ScopeRule:
		rule, _, _, err := findRule(current, target.RuleSetID, target.RuleID)
		if err != nil {
			return "", err
		}
		return formatRule(rule, 0), nil
	case ScopeGroup:
		group, _, _, _, err := findGroup(current, target.RuleSetID, target.RuleID, target.GroupID)
		if err != nil {
			return "", err
		}
		return "group " + formatGroupExpression(group, 0) + "\n", nil
	default:
		return "", fmt.Errorf("unsupported DSL scope %q", scope)
	}
}

func applyFragment(candidate *policy.WAFPolicy, previous policy.WAFPolicy, scope Scope, target Target, parsed any) error {
	switch scope {
	case ScopePolicy:
		next := parsed.(policy.WAFPolicy)
		reconcilePolicyIDs(&next, previous)
		*candidate = next
	case ScopeRuleSet:
		_, setIndex, err := findRuleSet(*candidate, target.RuleSetID)
		if err != nil {
			return err
		}
		old := candidate.RuleSets[setIndex]
		next := parsed.(policy.WAFRuleSet)
		reconcileRuleSetIDs(&next, old)
		candidate.RuleSets[setIndex] = next
	case ScopeRule:
		_, setIndex, ruleIndex, err := findRule(*candidate, target.RuleSetID, target.RuleID)
		if err != nil {
			return err
		}
		old := candidate.RuleSets[setIndex].Rules[ruleIndex]
		next := parsed.(policy.WAFRule)
		reconcileRuleIDs(&next, old)
		candidate.RuleSets[setIndex].Rules[ruleIndex] = next
	case ScopeGroup:
		_, setIndex, ruleIndex, groupIndex, err := findGroup(*candidate, target.RuleSetID, target.RuleID, target.GroupID)
		if err != nil {
			return err
		}
		old := candidate.RuleSets[setIndex].Rules[ruleIndex].Conditions.Groups[groupIndex]
		next := parsed.(policy.WAFConditionGroup)
		next.ID = old.ID
		reconcileConditionIDs(&next, old)
		candidate.RuleSets[setIndex].Rules[ruleIndex].Conditions.Groups[groupIndex] = next
	default:
		return fmt.Errorf("unsupported DSL scope %q", scope)
	}
	return nil
}

func reconcilePolicyIDs(next *policy.WAFPolicy, old policy.WAFPolicy) {
	sets := make(map[string]policy.WAFRuleSet, len(old.RuleSets))
	for _, set := range old.RuleSets {
		sets[set.ID] = set
	}
	for index := range next.RuleSets {
		if previous, ok := sets[next.RuleSets[index].ID]; ok {
			reconcileRuleSetIDs(&next.RuleSets[index], previous)
		} else {
			fillRuleSetIDs(&next.RuleSets[index])
		}
	}
}

func reconcileRuleSetIDs(next *policy.WAFRuleSet, old policy.WAFRuleSet) {
	rules := make(map[string]policy.WAFRule, len(old.Rules))
	for _, rule := range old.Rules {
		rules[rule.ID] = rule
	}
	for index := range next.Rules {
		if previous, ok := rules[next.Rules[index].ID]; ok {
			reconcileRuleIDs(&next.Rules[index], previous)
		} else {
			fillRuleIDs(&next.Rules[index])
		}
	}
}

func reconcileRuleIDs(next *policy.WAFRule, old policy.WAFRule) {
	used := make(map[int]bool, len(old.Conditions.Groups))
	for index := range next.Conditions.Groups {
		oldIndex := matchingGroupIndex(next.Conditions.Groups[index], old.Conditions.Groups, used)
		if oldIndex >= 0 {
			used[oldIndex] = true
			next.Conditions.Groups[index].ID = old.Conditions.Groups[oldIndex].ID
			reconcileConditionIDs(&next.Conditions.Groups[index], old.Conditions.Groups[oldIndex])
		} else {
			fillGroupIDs(&next.Conditions.Groups[index])
		}
	}
}

func reconcileConditionIDs(next *policy.WAFConditionGroup, old policy.WAFConditionGroup) {
	used := make(map[int]bool, len(old.Conditions))
	for index := range next.Conditions {
		oldIndex := matchingConditionIndex(next.Conditions[index], old.Conditions, used)
		if oldIndex >= 0 {
			used[oldIndex] = true
			next.Conditions[index].ID = old.Conditions[oldIndex].ID
		} else {
			next.Conditions[index].ID = uuid.NewString()
		}
	}
}

func matchingGroupIndex(next policy.WAFConditionGroup, old []policy.WAFConditionGroup, used map[int]bool) int {
	wanted := groupFingerprint(next)
	for index := range old {
		if !used[index] && groupFingerprint(old[index]) == wanted {
			return index
		}
	}
	for index := range old {
		if !used[index] {
			return index
		}
	}
	return -1
}

func matchingConditionIndex(next policy.WAFCondition, old []policy.WAFCondition, used map[int]bool) int {
	wanted := conditionFingerprint(next)
	for index := range old {
		if !used[index] && conditionFingerprint(old[index]) == wanted {
			return index
		}
	}
	for index := range old {
		if !used[index] {
			return index
		}
	}
	return -1
}

func groupFingerprint(value policy.WAFConditionGroup) string {
	value.ID = ""
	value.Conditions = append([]policy.WAFCondition(nil), value.Conditions...)
	for index := range value.Conditions {
		value.Conditions[index].ID = ""
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func conditionFingerprint(value policy.WAFCondition) string {
	value.ID = ""
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func fillRuleSetIDs(set *policy.WAFRuleSet) {
	for index := range set.Rules {
		fillRuleIDs(&set.Rules[index])
	}
}

func fillRuleIDs(rule *policy.WAFRule) {
	for index := range rule.Conditions.Groups {
		fillGroupIDs(&rule.Conditions.Groups[index])
	}
}

func fillGroupIDs(group *policy.WAFConditionGroup) {
	group.ID = uuid.NewString()
	for index := range group.Conditions {
		group.Conditions[index].ID = uuid.NewString()
	}
}

func findRuleSet(value policy.WAFPolicy, id string) (policy.WAFRuleSet, int, error) {
	for index, set := range value.RuleSets {
		if set.ID == id {
			return set, index, nil
		}
	}
	return policy.WAFRuleSet{}, -1, fmt.Errorf("rule set %q was not found", id)
}

func findRule(value policy.WAFPolicy, setID, ruleID string) (policy.WAFRule, int, int, error) {
	set, setIndex, err := findRuleSet(value, setID)
	if err != nil {
		return policy.WAFRule{}, -1, -1, err
	}
	for index, rule := range set.Rules {
		if rule.ID == ruleID {
			return rule, setIndex, index, nil
		}
	}
	return policy.WAFRule{}, -1, -1, fmt.Errorf("rule %q was not found", ruleID)
}

func findGroup(value policy.WAFPolicy, setID, ruleID, groupID string) (policy.WAFConditionGroup, int, int, int, error) {
	rule, setIndex, ruleIndex, err := findRule(value, setID, ruleID)
	if err != nil {
		return policy.WAFConditionGroup{}, -1, -1, -1, err
	}
	for index, group := range rule.Conditions.Groups {
		if group.ID == groupID {
			return group, setIndex, ruleIndex, index, nil
		}
	}
	return policy.WAFConditionGroup{}, -1, -1, -1, fmt.Errorf("condition group %q was not found", groupID)
}

func clonePolicy(value policy.WAFPolicy) policy.WAFPolicy {
	result := value
	result.TrustedProxies = append([]string(nil), value.TrustedProxies...)
	result.RuleSets = append([]policy.WAFRuleSet(nil), value.RuleSets...)
	for setIndex := range result.RuleSets {
		result.RuleSets[setIndex].Rules = append([]policy.WAFRule(nil), value.RuleSets[setIndex].Rules...)
		for ruleIndex := range result.RuleSets[setIndex].Rules {
			rule := &result.RuleSets[setIndex].Rules[ruleIndex]
			rule.Conditions.Groups = append([]policy.WAFConditionGroup(nil), rule.Conditions.Groups...)
			for groupIndex := range rule.Conditions.Groups {
				group := &rule.Conditions.Groups[groupIndex]
				group.Conditions = append([]policy.WAFCondition(nil), group.Conditions...)
				for conditionIndex := range group.Conditions {
					condition := &group.Conditions[conditionIndex]
					condition.Values = append([]string(nil), condition.Values...)
				}
			}
		}
	}
	return result
}
