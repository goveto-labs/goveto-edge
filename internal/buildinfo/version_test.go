package buildinfo

import "testing"

func TestCurrentNormalizesBuildVersion(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })

	for _, test := range []struct {
		value string
		want  string
	}{
		{"", "dev"},
		{"(devel)", "dev"},
		{" dev ", "dev"},
		{"1.2.3", "v1.2.3"},
		{"v1.2.3-rc.1+build.7", "v1.2.3-rc.1+build.7"},
	} {
		Version = test.value
		if got := Current(); got != test.want {
			t.Fatalf("Current() for %q = %q; want %q", test.value, got, test.want)
		}
	}
}

func TestIsReleaseAndNeedsUpgrade(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })
	Version = "v1.2.3"
	if !IsRelease() {
		t.Fatal("canonical version was not recognized as a release")
	}
	for _, test := range []struct {
		current string
		target  string
		want    bool
	}{
		{"dev", "v1.2.3", true},
		{"1.2.2", "v1.2.3", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.3.0", "v1.2.3", false},
		{"v1.2.2", "dev", false},
	} {
		if got := NeedsUpgrade(test.current, test.target); got != test.want {
			t.Fatalf("NeedsUpgrade(%q, %q) = %t; want %t", test.current, test.target, got, test.want)
		}
	}
}
