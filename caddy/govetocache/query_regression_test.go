package govetocache

import "testing"

func TestFilteredQueryPreservesParameterBoundariesAndOrder(t *testing.T) {
	for _, filter := range []struct{ include, exclude []string }{
		{include: []string{"x", "y"}}, {exclude: []string{"utm_*"}},
	} {
		for _, test := range []struct{ raw, want string }{
			{"x=a%26y%3Db", "x=a%26y%3Db"},
			{"x=a&y=b", "x=a&y=b"},
			{"y=1&x=b&x=a&utm_source=t", "y=1&x=b&x=a"},
			{"x=%2B&x=+&y=%25", "x=%2B&x=+&y=%25"},
		} {
			if got := normalizeQuery(test.raw, false, filter.include, filter.exclude); got != test.want {
				t.Errorf("normalizeQuery(%q) = %q, want %q", test.raw, got, test.want)
			}
		}
	}
}
