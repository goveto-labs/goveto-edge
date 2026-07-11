package cachematch

import (
	"net/http/httptest"
	"regexp"
	"testing"

	"goveto-edge/internal/policy"
)

func TestMatcherGroups(t *testing.T) {
	m := Matcher{Conditions: policy.CacheConditions{GroupOperator: "AND", Groups: []policy.CacheConditionGroup{
		{Operator: "OR", Rules: []policy.CacheConditionRule{{Type: "EXTENSION", Values: []string{"css"}}, {Type: "PATH_PREFIX", Values: []string{"/assets/"}}}},
		{Operator: "AND", Rules: []policy.CacheConditionRule{{Type: "PATH_REGEX", Value: `^/assets/`}}},
	}}}
	m.compiled = [][]*regexp.Regexp{{nil, nil}, {regexp.MustCompile(`^/assets/`)}}
	if !m.Match(httptest.NewRequest("GET", "http://example.test/assets/app.js", nil)) {
		t.Fatal("expected grouped condition to match")
	}
	if m.Match(httptest.NewRequest("GET", "http://example.test/app.css", nil)) {
		t.Fatal("expected outer AND to reject")
	}
}
