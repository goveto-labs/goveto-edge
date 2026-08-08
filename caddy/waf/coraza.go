package waf

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	coreruleset "github.com/corazawaf/coraza-coreruleset/v4"
	"github.com/corazawaf/coraza/v3"
	corazatypes "github.com/corazawaf/coraza/v3/types"

	"goveto-edge/internal/policy"
)

var sharedCRSEngines = struct {
	sync.Mutex
	engines map[int64]coraza.WAF
}{engines: map[int64]coraza.WAF{}}

var presetCRSTags = map[string][]string{
	"SQL_INJECTION":     {"attack-sqli"},
	"XSS":               {"attack-xss"},
	"PATH_TRAVERSAL":    {"attack-lfi", "attack-rfi", "attack-path-traversal"},
	"COMMAND_INJECTION": {"attack-rce", "attack-command-injection", "attack-injection-generic"},
	"SCANNER":           {"attack-reputation-scanner"},
}

func acquireCRSEngine(bodyLimit int64) (coraza.WAF, error) {
	sharedCRSEngines.Lock()
	defer sharedCRSEngines.Unlock()
	if engine := sharedCRSEngines.engines[bodyLimit]; engine != nil {
		return engine, nil
	}
	directives := fmt.Sprintf(`
Include @coraza.conf-recommended
SecRuleEngine DetectionOnly
SecRequestBodyAccess On
SecRequestBodyLimit %d
SecRequestBodyNoFilesLimit %d
SecRequestBodyLimitAction ProcessPartial
Include @crs-setup.conf.example
Include @owasp_crs/REQUEST-*.conf
`, bodyLimit, bodyLimit)
	engine, err := coraza.NewWAF(coraza.NewWAFConfig().
		WithRootFS(coreruleset.FS).
		WithRequestBodyAccess().
		WithRequestBodyLimit(int(bodyLimit)).
		WithRequestBodyInMemoryLimit(int(bodyLimit)).
		WithDirectives(directives))
	if err != nil {
		return nil, err
	}
	sharedCRSEngines.engines[bodyLimit] = engine
	return engine, nil
}

func (h Handler) matchCoraza(data requestData) (*wafDecision, error) {
	tx := h.crs.NewTransactionWithID(data.request.Header.Get("X-Request-ID"))
	defer tx.Close()
	r := data.request
	tx.ProcessConnection(data.ip, 0, "", 0)
	tx.ProcessURI(r.URL.RequestURI(), r.Method, r.Proto)
	for name, values := range r.Header {
		for _, value := range values {
			tx.AddRequestHeader(name, value)
		}
	}
	if r.Host != "" {
		tx.AddRequestHeader("Host", r.Host)
		tx.SetServerName(r.Host)
	}
	for _, encoding := range r.TransferEncoding {
		tx.AddRequestHeader("Transfer-Encoding", encoding)
	}
	tx.ProcessRequestHeaders()
	if data.body != "" {
		if _, _, err := tx.WriteRequestBody([]byte(data.body)); err != nil {
			return nil, fmt.Errorf("inspect request body with Coraza: %w", err)
		}
	}
	if _, err := tx.ProcessRequestBody(); err != nil {
		return nil, fmt.Errorf("evaluate Coraza request rules: %w", err)
	}

	enabled := stringSet(h.WAF.Presets)
	for _, matched := range tx.MatchedRules() {
		preset := presetForCRSRule(matched, enabled)
		if preset == "" {
			continue
		}
		ruleID := "crs:" + strconv.Itoa(matched.Rule().ID())
		if h.excepted(ruleID, data) || h.excepted("preset:"+preset, data) {
			continue
		}
		return &wafDecision{
			id:       ruleID,
			action:   policy.WAFActionShowPage,
			status:   h.WAF.BlockStatus,
			response: h.WAF.BlockResponse,
			source:   policy.WAFEngineCorazaCRS + ":" + h.WAF.RuleSetVersion,
			match:    crsMatchDetail(preset, matched),
		}, nil
	}
	return nil, nil
}

func presetForCRSRule(matched corazatypes.MatchedRule, enabled map[string]bool) string {
	tags := make(map[string]bool, len(matched.Rule().Tags()))
	for _, tag := range matched.Rule().Tags() {
		tags[strings.ToLower(tag)] = true
	}
	for _, preset := range []string{"SQL_INJECTION", "XSS", "PATH_TRAVERSAL", "COMMAND_INJECTION", "SCANNER"} {
		if !enabled[preset] {
			continue
		}
		for _, tag := range presetCRSTags[preset] {
			if tags[tag] {
				return preset
			}
		}
	}
	return ""
}

func crsMatchDetail(preset string, matched corazatypes.MatchedRule) string {
	details := []string{"preset=" + preset, "rule=" + strconv.Itoa(matched.Rule().ID())}
	if values := matched.MatchedDatas(); len(values) > 0 {
		value := values[0]
		variable := fmt.Sprint(value.Variable())
		if key := strings.TrimSpace(value.Key()); key != "" {
			variable += ":" + sanitizeMatchText(key, false)
		}
		details = append(details, "variable="+variable, "match="+sanitizeMatchText(value.Value(), sensitiveMatchKey(variable)))
	}
	return strings.Join(details, ";")
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
	const limit = 96
	characters := []rune(value)
	if len(characters) > limit {
		value = string(characters[:limit]) + "..."
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
