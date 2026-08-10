package dns

import (
	"encoding/json"
	"strings"
	"testing"

	"goveto-edge/internal/storage/gen/model"
)

// TestEncodeProviderCredentials sanitizes and validates the credential maps
// before they are ever encrypted. Provider errors here would leak malformed
// blobs into the encrypted store, so every branch matters.
func TestEncodeProviderCredentials(t *testing.T) {
	t.Run("aliyun trims and keeps only the two required keys", func(t *testing.T) {
		raw, err := encodeProviderCredentials(model.DNSProviderTypeALIYUN, map[string]string{
			"access_key_id":     "  AKID  ",
			"access_key_secret": "secret",
			"stray":             "ignored",
		})
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded) != 2 || decoded["access_key_id"] != "AKID" || decoded["access_key_secret"] != "secret" {
			t.Fatalf("unexpected aliyun payload: %s", raw)
		}
	})

	t.Run("cloudflare requires api_token", func(t *testing.T) {
		raw, err := encodeProviderCredentials(model.DNSProviderTypeCLOUDFLARE, map[string]string{
			"api_token": "tok",
		})
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["api_token"] != "tok" || len(decoded) != 1 {
			t.Fatalf("unexpected cloudflare payload: %s", raw)
		}
	})

	for _, test := range []struct {
		name     string
		provider model.DNSProviderType
		input    map[string]string
	}{
		{"aliyun missing id", model.DNSProviderTypeALIYUN, map[string]string{"access_key_secret": "s"}},
		{"aliyun missing secret", model.DNSProviderTypeALIYUN, map[string]string{"access_key_id": "id"}},
		{"aliyun blank id", model.DNSProviderTypeALIYUN, map[string]string{"access_key_id": "  ", "access_key_secret": "s"}},
		{"cloudflare empty token", model.DNSProviderTypeCLOUDFLARE, map[string]string{"api_token": "   "}},
		{"cloudflare empty map", model.DNSProviderTypeCLOUDFLARE, map[string]string{}},
		{"empty map", model.DNSProviderTypeALIYUN, map[string]string{}},
		{"unknown provider", "ROUTE53", map[string]string{"anything": "x"}},
	} {
		t.Run(test.name+" rejected", func(t *testing.T) {
			if _, err := encodeProviderCredentials(test.provider, test.input); err == nil {
				t.Fatalf("expected error for %v", test.input)
			}
		})
	}
}

// TestUniqueLineName covers the provider-line deduplication used by the
// concurrent DNS sync reconciler. When two provider lines share a display
// name, the second must be suffixed so neither overwrites the other; a name
// already owned by the same code is returned unchanged.
func TestUniqueLineName(t *testing.T) {
	t.Run("returns unchanged when free or owned by same code", func(t *testing.T) {
		names := map[string]string{}
		if got := uniqueLineName("Default", "default", names); got != "Default" {
			t.Fatalf("free name changed: %q", got)
		}
		names["Default"] = "default"
		if got := uniqueLineName("Default", "default", names); got != "Default" {
			t.Fatalf("same-owner name changed: %q", got)
		}
	})

	t.Run("suffixes collisions per code and appends on secondary clash", func(t *testing.T) {
		names := map[string]string{"Default": "telecom"}
		// telecom already owns the bare name; every other code gets its own suffix.
		if got := uniqueLineName("Default", "unicom", names); got != "Default (unicom)" {
			t.Fatalf("unicom suffix wrong: %q", got)
		} else {
			names[got] = "unicom"
		}
		if got := uniqueLineName("Default", "mobile", names); got != "Default (mobile)" {
			t.Fatalf("mobile suffix wrong: %q", got)
		} else {
			names[got] = "mobile"
		}
		// Re-running the same provider line must resolve to the same name so a
		// reconciliation does not keep appending suffixes to itself.
		if got := uniqueLineName("Default", "unicom", names); got != "Default (unicom)" {
			t.Fatalf("idempotent re-run diverged: %q", got)
		}
		// The `-` dedup branch fires only when the suffixed name is itself held
		// by a different code (an out-of-band clash).
		names["Default (unicom)"] = "telecom"
		got := uniqueLineName("Default", "unicom", names)
		if got == "Default (unicom)" {
			t.Fatalf("secondary clash was not pushed aside: %q", got)
		}
		if !strings.HasPrefix(got, "Default (unicom)") {
			t.Fatalf("secondary clash lost its base: %q", got)
		}
	})
}

func TestOptionalCreateString(t *testing.T) {
	if got := optionalCreateString(""); got != nil {
		t.Fatalf("empty input should yield nil, got %v", got)
	}
	got := optionalCreateString("parent")
	if got == nil || **got != "parent" {
		t.Fatalf("non-empty input not wrapped: %v", got)
	}
}
