package node

import "testing"

func TestShellQuote(t *testing.T) {
	if got, want := shellQuote("it's safe"), `'it'\''s safe'`; got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func TestNormalizeArchitecture(t *testing.T) {
	for input, want := range map[string]string{"x86_64\n": "amd64", "amd64": "amd64", "aarch64\n": "arm64", "arm64": "arm64"} {
		got, err := normalizeArchitecture(input)
		if err != nil || got != want {
			t.Fatalf("normalizeArchitecture(%q) = %q, %v", input, got, err)
		}
	}
	if _, err := normalizeArchitecture("riscv64"); err == nil {
		t.Fatal("expected unsupported architecture error")
	}
}
