package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const (
	WAFEngineGovetoCompat = "GOVETO_COMPAT"
	WAFEngineCorazaCRS    = "CORAZA_CRS"
	WAFModeBlock          = "BLOCK"
	WAFModeMonitor        = "MONITOR"

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

// KnownWAFRuleSetVersions is the ordered list of rule set versions the
// engine accepts, oldest first. The final entry is the latest version and
// the target for AutoUpdate. Adding a version here is the only change
// needed to ship a new rule set: validation accepts any listed version,
// and agents with AutoUpdate enabled normalize up to LatestWAFRuleSetVersion.
var KnownWAFRuleSetVersions = []string{"2026.07.1"}

const (
	// LatestWAFRuleSetVersion is the newest compatibility rule set version.
	LatestWAFRuleSetVersion = "2026.07.1"
	// LatestCorazaCRSVersion is the embedded OWASP Core Rule Set version.
	LatestCorazaCRSVersion = "4.25.0"
)

// CurrentWAFRuleSetVersion is the newest known rule set version (alias).
const CurrentWAFRuleSetVersion = LatestWAFRuleSetVersion

var knownWAFRuleSetVersions = func() map[string]int {
	order := map[string]int{}
	for index, version := range KnownWAFRuleSetVersions {
		order[version] = index
	}
	return order
}()

var supportedWAFPresets = map[string]struct{}{
	"BAD_BOTS":          {},
	"COMMAND_INJECTION": {},
	"PATH_TRAVERSAL":    {},
	"SCANNER":           {},
	"SQL_INJECTION":     {},
	"XSS":               {},
}

type WAFPolicy struct {
	Enabled           bool           `json:"enabled"`
	Engine            string         `json:"engine"`
	RuleSetVersion    string         `json:"rule_set_version"`
	AutoUpdate        bool           `json:"auto_update"`
	RolloutPercentage int            `json:"rollout_percentage"`
	Mode              string         `json:"mode"`
	BlockStatus       int            `json:"block_status"`
	BlockResponse     WAFResponse    `json:"block_response"`
	MaxBodyBytes      int64          `json:"max_body_bytes"`
	Presets           []string       `json:"presets"`
	Groups            []WAFRuleGroup `json:"groups"`
	Exceptions        []WAFException `json:"exceptions"`
}

type WAFRuleGroup struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Enabled           bool             `json:"enabled"`
	RolloutPercentage int              `json:"rollout_percentage"`
	Operator          string           `json:"operator"`
	Action            string           `json:"action"`
	StatusCode        int              `json:"status_code,omitempty"`
	Response          WAFResponse      `json:"response,omitempty"`
	RedirectURL       string           `json:"redirect_url,omitempty"`
	RedirectStatus    int              `json:"redirect_status,omitempty"`
	Tag               string           `json:"tag,omitempty"`
	AutoBan           WAFAutoBan       `json:"auto_ban,omitempty"`
	Rules             []WAFRequestRule `json:"rules"`
}

// WAFAutoBan closes the loop between detection and enforcement: after a
// group fires Hits times within WindowSeconds for the same client IP, the
// edge agent writes a temporary block (site-scoped or global) that subsequent
// requests consult before re-evaluating the WAF. This turns a noisy repeat
// offender into an enforced ban without a control-plane round-trip.
//
// Hits are only accrued for enforcement actions (BLOCK, SHOW_PAGE, REDIRECT,
// CAPTCHA). MONITOR and TAG never count, and the entire auto-ban path is
// idle while the engine Mode is MONITOR so observation-only deploys cannot
// hard-block clients.
type WAFAutoBan struct {
	Enabled       bool   `json:"enabled"`
	Hits          int    `json:"hits"`
	WindowSeconds int    `json:"window_seconds"`
	BanSeconds    int    `json:"ban_seconds"`
	Scope         string `json:"scope,omitempty"`
}

type WAFException struct {
	ID         string            `json:"id"`
	Enabled    bool              `json:"enabled"`
	RuleIDs    []string          `json:"rule_ids"`
	Conditions RequestConditions `json:"conditions"`
}

type WAFResponse struct {
	Type string `json:"type"`
	Body string `json:"body,omitempty"`
}

type WAFRequestRule struct {
	Field         string   `json:"field"`
	Name          string   `json:"name,omitempty"`
	Operator      string   `json:"operator"`
	Value         string   `json:"value,omitempty"`
	Values        []string `json:"values,omitempty"`
	Negate        bool     `json:"negate,omitempty"`
	CaseSensitive bool     `json:"case_sensitive,omitempty"`
}

type RateLimitPolicy struct {
	Enabled     bool            `json:"enabled"`
	Backend     string          `json:"backend"`
	FailureMode string          `json:"failure_mode"`
	Rules       []RateLimitRule `json:"rules"`
}

type RateLimitRule struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Enabled       bool              `json:"enabled"`
	Key           string            `json:"key"`
	KeyName       string            `json:"key_name,omitempty"`
	Requests      int               `json:"requests"`
	WindowSeconds int               `json:"window_seconds"`
	Burst         int               `json:"burst"`
	BanSeconds    int               `json:"ban_seconds"`
	StatusCode    int               `json:"status_code"`
	Conditions    RequestConditions `json:"conditions"`
}

type RequestConditions struct {
	GroupOperator string                  `json:"group_operator"`
	Groups        []RequestConditionGroup `json:"groups"`
}

type RequestConditionGroup struct {
	Operator string           `json:"operator"`
	Rules    []WAFRequestRule `json:"rules"`
}

func DefaultWAFPolicy() WAFPolicy {
	return WAFPolicy{
		Enabled: true,
		Engine:  WAFEngineGovetoCompat, RuleSetVersion: CurrentWAFRuleSetVersion,
		AutoUpdate: true, RolloutPercentage: 100,
		Mode:          WAFModeBlock,
		BlockStatus:   http.StatusForbidden,
		BlockResponse: WAFResponse{Type: WAFResponseDefault},
		MaxBodyBytes:  64 << 10,
		Presets:       []string{"SQL_INJECTION", "XSS", "PATH_TRAVERSAL", "COMMAND_INJECTION", "SCANNER", "BAD_BOTS"},
		Groups:        []WAFRuleGroup{},
		Exceptions:    []WAFException{},
	}
}

func DefaultRateLimitPolicy() RateLimitPolicy {
	return RateLimitPolicy{Backend: "LOCAL", FailureMode: "LOCAL", Rules: []RateLimitRule{}}
}

func (p *WAFPolicy) NormalizeAndValidate() error {
	p.Engine = strings.ToUpper(strings.TrimSpace(p.Engine))
	if p.Engine == "" {
		p.Engine = WAFEngineGovetoCompat
	}
	if p.Engine != WAFEngineGovetoCompat && p.Engine != WAFEngineCorazaCRS {
		return fmt.Errorf("unsupported WAF engine %q", p.Engine)
	}
	p.RuleSetVersion = strings.TrimSpace(p.RuleSetVersion)
	if p.AutoUpdate || p.RuleSetVersion == "" {
		// AutoUpdate normalizes empty and older versions up to the latest
		// known rule set, so agents roll forward without a config push.
		p.RuleSetVersion = latestWAFRuleSetVersion(p.Engine)
	}
	if !knownWAFRuleSetVersion(p.Engine, p.RuleSetVersion) {
		return fmt.Errorf("unsupported WAF rule set version %q", p.RuleSetVersion)
	}
	if p.RolloutPercentage == 0 {
		p.RolloutPercentage = 100
	}
	if p.RolloutPercentage < 1 || p.RolloutPercentage > 100 {
		return errors.New("rollout_percentage must be between 1 and 100")
	}
	p.Mode = strings.ToUpper(strings.TrimSpace(p.Mode))
	if p.Mode == "" {
		p.Mode = WAFModeBlock
	}
	if p.Mode != WAFModeBlock && p.Mode != WAFModeMonitor {
		return errors.New("mode must be BLOCK or MONITOR")
	}
	if p.BlockStatus == 0 {
		p.BlockStatus = http.StatusForbidden
	}
	if p.BlockStatus < 400 || p.BlockStatus > 599 {
		return errors.New("block_status must be between 400 and 599")
	}
	if p.MaxBodyBytes == 0 {
		p.MaxBodyBytes = 64 << 10
	}
	if p.MaxBodyBytes < 0 || p.MaxBodyBytes > 1<<20 {
		return errors.New("max_body_bytes must be between 0 and 1048576")
	}

	seenPresets := map[string]struct{}{}
	for index, preset := range p.Presets {
		preset = strings.ToUpper(strings.TrimSpace(preset))
		if _, ok := supportedWAFPresets[preset]; !ok {
			return fmt.Errorf("unsupported WAF preset %q", preset)
		}
		if _, ok := seenPresets[preset]; ok {
			return fmt.Errorf("duplicate WAF preset %q", preset)
		}
		seenPresets[preset] = struct{}{}
		p.Presets[index] = preset
	}
	sort.Strings(p.Presets)
	if p.Presets == nil {
		p.Presets = []string{}
	}
	if p.Groups == nil {
		p.Groups = []WAFRuleGroup{}
	}
	if p.Exceptions == nil {
		p.Exceptions = []WAFException{}
	}

	if len(p.Groups) > 64 {
		return errors.New("WAF policy cannot contain more than 64 groups")
	}
	seenIDs := map[string]struct{}{}
	for index := range p.Groups {
		group := &p.Groups[index]
		group.ID = strings.TrimSpace(group.ID)
		if group.ID == "" {
			group.ID = fmt.Sprintf("group-%d", index+1)
		}
		if _, ok := seenIDs[group.ID]; ok {
			return fmt.Errorf("duplicate WAF group id %q", group.ID)
		}
		seenIDs[group.ID] = struct{}{}
		group.Name = strings.TrimSpace(group.Name)
		if group.RolloutPercentage == 0 {
			group.RolloutPercentage = 100
		}
		if group.RolloutPercentage < 1 || group.RolloutPercentage > 100 {
			return fmt.Errorf("groups[%d].rollout_percentage must be between 1 and 100", index)
		}
		group.Operator = normalizeBooleanOperator(group.Operator)
		if !booleanOperator(group.Operator) {
			return fmt.Errorf("groups[%d].operator must be AND or OR", index)
		}
		group.Action = strings.ToUpper(strings.TrimSpace(group.Action))
		if group.Action == "" {
			group.Action = "BLOCK"
		}
		switch group.Action {
		case WAFActionShowPage, WAFActionBlock, WAFActionCaptcha, WAFActionRedirect, WAFActionAllow, WAFActionTag, WAFActionMonitor:
		default:
			return fmt.Errorf("groups[%d].action is unsupported", index)
		}
		if group.StatusCode == 0 {
			group.StatusCode = p.BlockStatus
		}
		if (group.Action == WAFActionShowPage || group.Action == WAFActionBlock) && (group.StatusCode < 400 || group.StatusCode > 599) {
			return fmt.Errorf("groups[%d].status_code must be between 400 and 599", index)
		}
		if err := normalizeWAFResponse(&group.Response, fmt.Sprintf("groups[%d].response", index)); err != nil {
			return err
		}
		if group.Action == WAFActionRedirect {
			if err := normalizeRedirect(group, index); err != nil {
				return err
			}
		}
		if group.Action == WAFActionTag {
			group.Tag = strings.TrimSpace(group.Tag)
			if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`).MatchString(group.Tag) {
				return fmt.Errorf("groups[%d].tag must be 1-64 safe characters", index)
			}
		}
		if err := normalizeWAFAutoBan(&group.AutoBan, fmt.Sprintf("groups[%d].auto_ban", index)); err != nil {
			return err
		}
		if len(group.Rules) == 0 || len(group.Rules) > 64 {
			return fmt.Errorf("groups[%d] must contain between 1 and 64 rules", index)
		}
		if err := normalizeRequestRules(group.Rules, fmt.Sprintf("groups[%d]", index)); err != nil {
			return err
		}
	}
	if len(p.Exceptions) > 64 {
		return errors.New("WAF policy cannot contain more than 64 exceptions")
	}
	seenExceptions := map[string]struct{}{}
	for index := range p.Exceptions {
		exception := &p.Exceptions[index]
		exception.ID = strings.TrimSpace(exception.ID)
		if exception.ID == "" {
			exception.ID = fmt.Sprintf("exception-%d", index+1)
		}
		if _, exists := seenExceptions[exception.ID]; exists {
			return fmt.Errorf("duplicate WAF exception id %q", exception.ID)
		}
		seenExceptions[exception.ID] = struct{}{}
		if len(exception.RuleIDs) == 0 || len(exception.RuleIDs) > 64 {
			return fmt.Errorf("exceptions[%d].rule_ids must contain between 1 and 64 values", index)
		}
		for ruleIndex := range exception.RuleIDs {
			exception.RuleIDs[ruleIndex] = strings.TrimSpace(exception.RuleIDs[ruleIndex])
			if exception.RuleIDs[ruleIndex] == "" {
				return fmt.Errorf("exceptions[%d].rule_ids contains an empty value", index)
			}
		}
		if err := normalizeConditions(&exception.Conditions, fmt.Sprintf("exceptions[%d].conditions", index)); err != nil {
			return err
		}
	}
	if err := normalizeWAFResponse(&p.BlockResponse, "block_response"); err != nil {
		return err
	}
	return nil
}

func latestWAFRuleSetVersion(engine string) string {
	if engine == WAFEngineCorazaCRS {
		return LatestCorazaCRSVersion
	}
	return LatestWAFRuleSetVersion
}

func knownWAFRuleSetVersion(engine, version string) bool {
	if engine == WAFEngineCorazaCRS {
		return version == LatestCorazaCRSVersion
	}
	_, ok := knownWAFRuleSetVersions[version]
	return ok
}

func normalizeWAFAutoBan(ban *WAFAutoBan, location string) error {
	ban.Scope = strings.ToUpper(strings.TrimSpace(ban.Scope))
	if !ban.Enabled {
		if ban.Scope == "" {
			ban.Scope = "SITE"
		}
		return nil
	}
	if ban.Hits < 1 || ban.Hits > 100000 {
		return fmt.Errorf("%s.hits must be between 1 and 100000", location)
	}
	if ban.WindowSeconds < 1 || ban.WindowSeconds > 86400 {
		return fmt.Errorf("%s.window_seconds must be between 1 and 86400", location)
	}
	if ban.BanSeconds < 1 || ban.BanSeconds > 86400 {
		return fmt.Errorf("%s.ban_seconds must be between 1 and 86400", location)
	}
	if ban.Scope == "" {
		ban.Scope = "SITE"
	}
	if ban.Scope != "SITE" && ban.Scope != "GLOBAL" {
		return fmt.Errorf("%s.scope must be SITE or GLOBAL", location)
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

func normalizeRedirect(group *WAFRuleGroup, index int) error {
	group.RedirectURL = strings.TrimSpace(group.RedirectURL)
	if strings.ContainsAny(group.RedirectURL, "\r\n") || group.RedirectURL == "" {
		return fmt.Errorf("groups[%d].redirect_url is invalid", index)
	}
	parsed, err := url.Parse(group.RedirectURL)
	if err != nil || (parsed.IsAbs() && parsed.Scheme != "http" && parsed.Scheme != "https") || (!parsed.IsAbs() && !strings.HasPrefix(group.RedirectURL, "/")) {
		return fmt.Errorf("groups[%d].redirect_url must be an HTTP(S) URL or absolute path", index)
	}
	if group.RedirectStatus == 0 {
		group.RedirectStatus = http.StatusFound
	}
	switch group.RedirectStatus {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return nil
	default:
		return fmt.Errorf("groups[%d].redirect_status is unsupported", index)
	}
}

func (p *RateLimitPolicy) NormalizeAndValidate() error {
	p.Backend = strings.ToUpper(strings.TrimSpace(p.Backend))
	if p.Backend == "" {
		p.Backend = "LOCAL"
	}
	if p.Backend != "LOCAL" && p.Backend != "REDIS" {
		return errors.New("rate-limit backend must be LOCAL or REDIS")
	}
	p.FailureMode = strings.ToUpper(strings.TrimSpace(p.FailureMode))
	if p.FailureMode == "" {
		p.FailureMode = "LOCAL"
	}
	if p.FailureMode != "OPEN" && p.FailureMode != "CLOSED" && p.FailureMode != "LOCAL" {
		return errors.New("rate-limit failure_mode must be OPEN, CLOSED or LOCAL")
	}
	if p.Rules == nil {
		p.Rules = []RateLimitRule{}
	}
	if len(p.Rules) > 64 {
		return errors.New("rate-limit policy cannot contain more than 64 rules")
	}
	seenIDs := map[string]struct{}{}
	for index := range p.Rules {
		rule := &p.Rules[index]
		rule.ID = strings.TrimSpace(rule.ID)
		if rule.ID == "" {
			rule.ID = fmt.Sprintf("cc-%d", index+1)
		}
		if _, ok := seenIDs[rule.ID]; ok {
			return fmt.Errorf("duplicate rate-limit rule id %q", rule.ID)
		}
		seenIDs[rule.ID] = struct{}{}
		rule.Name = strings.TrimSpace(rule.Name)
		rule.Key = strings.ToUpper(strings.TrimSpace(rule.Key))
		switch rule.Key {
		case "CLIENT_IP", "CLIENT_IP_PATH", "PATH", "GLOBAL":
			rule.KeyName = ""
		case "HEADER":
			rule.KeyName = http.CanonicalHeaderKey(strings.TrimSpace(rule.KeyName))
			if rule.KeyName == "" {
				return fmt.Errorf("rules[%d].key_name is required for HEADER", index)
			}
		case "COOKIE":
			rule.KeyName = strings.TrimSpace(rule.KeyName)
			if rule.KeyName == "" {
				return fmt.Errorf("rules[%d].key_name is required for COOKIE", index)
			}
		default:
			return fmt.Errorf("rules[%d].key is unsupported", index)
		}
		if rule.Requests < 1 || rule.Requests > 1_000_000 {
			return fmt.Errorf("rules[%d].requests must be between 1 and 1000000", index)
		}
		if rule.WindowSeconds < 1 || rule.WindowSeconds > 3600 {
			return fmt.Errorf("rules[%d].window_seconds must be between 1 and 3600", index)
		}
		if rule.Burst < 0 || rule.Burst > rule.Requests*10 {
			return fmt.Errorf("rules[%d].burst must be between 0 and requests*10", index)
		}
		if rule.BanSeconds < 0 || rule.BanSeconds > 86400 {
			return fmt.Errorf("rules[%d].ban_seconds must be between 0 and 86400", index)
		}
		if rule.StatusCode == 0 {
			rule.StatusCode = http.StatusTooManyRequests
		}
		if rule.StatusCode < 400 || rule.StatusCode > 599 {
			return fmt.Errorf("rules[%d].status_code must be between 400 and 599", index)
		}
		if err := normalizeConditions(&rule.Conditions, fmt.Sprintf("rules[%d].conditions", index)); err != nil {
			return err
		}
	}
	return nil
}

func normalizeConditions(conditions *RequestConditions, location string) error {
	conditions.GroupOperator = normalizeBooleanOperator(conditions.GroupOperator)
	if !booleanOperator(conditions.GroupOperator) {
		return fmt.Errorf("%s.group_operator must be AND or OR", location)
	}
	if len(conditions.Groups) > 16 {
		return fmt.Errorf("%s cannot contain more than 16 groups", location)
	}
	for index := range conditions.Groups {
		group := &conditions.Groups[index]
		group.Operator = normalizeBooleanOperator(group.Operator)
		if !booleanOperator(group.Operator) {
			return fmt.Errorf("%s.groups[%d].operator must be AND or OR", location, index)
		}
		if len(group.Rules) == 0 || len(group.Rules) > 64 {
			return fmt.Errorf("%s.groups[%d] must contain between 1 and 64 rules", location, index)
		}
		if err := normalizeRequestRules(group.Rules, fmt.Sprintf("%s.groups[%d]", location, index)); err != nil {
			return err
		}
	}
	return nil
}

func normalizeRequestRules(rules []WAFRequestRule, location string) error {
	for index := range rules {
		rule := &rules[index]
		rule.Field = strings.ToUpper(strings.TrimSpace(rule.Field))
		rule.Operator = strings.ToUpper(strings.TrimSpace(rule.Operator))
		rule.Name = strings.TrimSpace(rule.Name)
		rule.Value = strings.TrimSpace(rule.Value)
		for valueIndex := range rule.Values {
			rule.Values[valueIndex] = strings.TrimSpace(rule.Values[valueIndex])
		}

		switch rule.Field {
		case "METHOD", "HOST", "PATH", "RAW_QUERY", "BODY", "CLIENT_IP", "USER_AGENT":
		case "QUERY", "COOKIE":
			if rule.Name == "" {
				return fmt.Errorf("%s.rules[%d].name is required for %s", location, index, rule.Field)
			}
		case "HEADER":
			rule.Name = http.CanonicalHeaderKey(rule.Name)
			if rule.Name == "" {
				return fmt.Errorf("%s.rules[%d].name is required for HEADER", location, index)
			}
		default:
			return fmt.Errorf("%s.rules[%d].field %q is unsupported", location, index, rule.Field)
		}

		switch rule.Operator {
		case "EXISTS":
			rule.Value, rule.Values = "", nil
		case "EQUALS", "CONTAINS", "PREFIX", "SUFFIX", "REGEX":
			if rule.Value == "" {
				return fmt.Errorf("%s.rules[%d].value is required", location, index)
			}
			if rule.Operator == "REGEX" {
				if _, err := regexp.Compile(rule.Value); err != nil {
					return fmt.Errorf("%s.rules[%d] has invalid regex: %w", location, index, err)
				}
			}
		case "IN":
			if len(rule.Values) == 0 {
				return fmt.Errorf("%s.rules[%d].values is required for IN", location, index)
			}
		case "CIDR":
			if rule.Field != "CLIENT_IP" {
				return fmt.Errorf("%s.rules[%d] CIDR only supports CLIENT_IP", location, index)
			}
			values := rule.Values
			if rule.Value != "" {
				values = append(values, rule.Value)
			}
			if len(values) == 0 {
				return fmt.Errorf("%s.rules[%d] requires at least one CIDR", location, index)
			}
			for _, value := range values {
				if _, err := netip.ParsePrefix(value); err != nil {
					return fmt.Errorf("%s.rules[%d] has invalid CIDR %q", location, index, value)
				}
			}
			rule.Values, rule.Value = values, ""
		default:
			return fmt.Errorf("%s.rules[%d].operator %q is unsupported", location, index, rule.Operator)
		}
	}
	return nil
}

func normalizeBooleanOperator(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return "AND"
	}
	return value
}
