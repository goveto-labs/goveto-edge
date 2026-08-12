package wafdsl

import (
	"strconv"
	"strings"

	"goveto-edge/internal/policy"
)

func formatPolicy(value policy.WAFPolicy) string {
	var out strings.Builder
	out.WriteString("waf " + enabledWord(value.Enabled) + " {\n")
	out.WriteString("  trusted_proxy_chain " + enabledWord(value.TrustedProxyChain) + "\n")
	out.WriteString("  trusted_proxies " + formatSet(value.TrustedProxies, true) + "\n")
	for _, set := range value.RuleSets {
		out.WriteString("\n")
		out.WriteString(formatRuleSet(set, 2))
	}
	out.WriteString("}\n")
	return out.String()
}

func formatRuleSet(value policy.WAFRuleSet, indent int) string {
	pad := strings.Repeat(" ", indent)
	var out strings.Builder
	out.WriteString(pad + "ruleset " + strconv.Quote(value.ID) + " " + strconv.Quote(value.Name) + " " + enabledWord(value.Enabled) + " {\n")
	for index, rule := range value.Rules {
		if index > 0 {
			out.WriteString("\n")
		}
		out.WriteString(formatRule(rule, indent+2))
	}
	out.WriteString(pad + "}\n")
	return out.String()
}

func formatRule(value policy.WAFRule, indent int) string {
	pad := strings.Repeat(" ", indent)
	kind := "rule"
	if value.Type == policy.WAFRuleTypeRateLimit {
		kind = "rate_limit"
	}
	var out strings.Builder
	out.WriteString(pad + kind + " " + strconv.Quote(value.ID) + " " + strconv.Quote(value.Name) + " " + enabledWord(value.Enabled) + " {\n")
	if len(value.Conditions.Groups) > 0 {
		out.WriteString(formatConditions(value.Conditions, indent+2))
	}
	if value.Type == policy.WAFRuleTypeRateLimit {
		out.WriteString(strings.Repeat(" ", indent+2) + "count_by " + formatRateKey(value) + "\n")
		out.WriteString(strings.Repeat(" ", indent+2) + "limit " + strconv.Itoa(value.Requests) + " per " + strconv.Itoa(value.WindowSeconds) + "s burst " + strconv.Itoa(value.Burst) + "\n")
		backend := strings.ToLower(value.Backend)
		if backend == "" {
			backend = "local"
		}
		out.WriteString(strings.Repeat(" ", indent+2) + "backend " + backend)
		if backend == "redis" {
			fallback := strings.ToLower(value.FailureMode)
			if fallback == "" {
				fallback = "local"
			}
			out.WriteString(" fallback " + fallback)
		}
		out.WriteString("\n")
	}
	out.WriteString(strings.Repeat(" ", indent+2) + "then " + formatAction(value.Action) + "\n")
	out.WriteString(pad + "}\n")
	return out.String()
}

func formatConditions(value policy.WAFConditions, indent int) string {
	pad := strings.Repeat(" ", indent)
	var out strings.Builder
	out.WriteString(pad + "when (\n")
	connector := " " + strings.ToLower(value.Operator) + "\n"
	for index, group := range value.Groups {
		if index > 0 {
			out.WriteString(connector)
		}
		out.WriteString(strings.Repeat(" ", indent+2) + formatGroupExpression(group, indent+2))
	}
	out.WriteString("\n" + pad + ")\n")
	return out.String()
}

func formatGroupExpression(value policy.WAFConditionGroup, indent int) string {
	if len(value.Conditions) == 0 {
		return "()"
	}
	connector := " " + strings.ToLower(value.Operator) + "\n" + strings.Repeat(" ", indent+2)
	parts := make([]string, len(value.Conditions))
	for index, condition := range value.Conditions {
		parts[index] = formatCondition(condition)
	}
	return "(" + strings.Join(parts, connector) + ")"
}

func formatCondition(value policy.WAFCondition) string {
	var out strings.Builder
	if value.Negate {
		out.WriteString("not ")
	}
	out.WriteString(fieldName(value))
	operators := map[string]string{"EXISTS": "exists", "EQUALS": "eq", "CONTAINS": "contains", "PREFIX": "starts_with", "SUFFIX": "ends_with", "REGEX": "matches", "IN": "in", "CIDR": "in"}
	out.WriteString(" " + operators[value.Operator])
	switch value.Operator {
	case "EQUALS", "CONTAINS", "PREFIX", "SUFFIX", "REGEX":
		out.WriteString(" " + strconv.Quote(value.Value))
	case "IN":
		out.WriteString(" " + formatSet(value.Values, false))
	case "CIDR":
		out.WriteString(" " + formatSet(value.Values, true))
	}
	if value.CaseSensitive {
		out.WriteString(" case_sensitive")
	}
	return out.String()
}

func formatAction(value policy.WAFAction) string {
	switch value.Type {
	case policy.WAFActionBlock:
		return "block status " + strconv.Itoa(value.StatusCode)
	case policy.WAFActionShowPage:
		responseType := strings.ToLower(value.Response.Type)
		if responseType == "" {
			responseType = "default"
		}
		result := "show_page status " + strconv.Itoa(value.StatusCode) + " response " + responseType
		if responseType != "default" {
			result += " " + strconv.Quote(value.Response.Body)
		}
		return result
	case policy.WAFActionCaptcha:
		return "captcha"
	case policy.WAFActionAllow:
		return "allow"
	case policy.WAFActionMonitor:
		return "monitor"
	case policy.WAFActionRedirect:
		return "redirect " + strconv.Quote(value.RedirectURL) + " status " + strconv.Itoa(value.RedirectStatus)
	case policy.WAFActionTag:
		return "tag " + strconv.Quote(value.Tag)
	default:
		return strings.ToLower(value.Type)
	}
}

func formatRateKey(value policy.WAFRule) string {
	switch value.Key {
	case "CLIENT_IP_PATH":
		return "ip.src + http.request.uri.path"
	case "CLIENT_IP":
		return "ip.src"
	case "PATH":
		return "http.request.uri.path"
	case "GLOBAL":
		return "global"
	case "HEADER":
		return "http.request.headers[" + strconv.Quote(value.KeyName) + "]"
	case "COOKIE":
		return "http.request.cookies[" + strconv.Quote(value.KeyName) + "]"
	default:
		return strings.ToLower(value.Key)
	}
}

func formatSet(values []string, raw bool) string {
	parts := make([]string, len(values))
	for index, value := range values {
		if raw {
			parts[index] = value
		} else {
			parts[index] = strconv.Quote(value)
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func enabledWord(value bool) string {
	if value {
		return "enabled"
	}
	return "disabled"
}
