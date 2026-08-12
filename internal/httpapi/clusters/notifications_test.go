package clusters

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

type notificationRoundTripper func(*http.Request) (*http.Response, error)

func (f notificationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestValidateNotificationChannelInput(t *testing.T) {
	enabled := true
	tests := []struct {
		name        string
		input       notificationChannelRequest
		requireURL  bool
		wantService string
		wantError   bool
	}{
		{name: "logger", input: notificationChannelRequest{Name: "Primary", URL: "logger://", Enabled: &enabled}, requireURL: true, wantService: "logger"},
		{name: "custom generic HTTPS", input: notificationChannelRequest{Name: "Webhook", URL: "generic+https://example.com/hook"}, requireURL: true, wantService: "generic"},
		{name: "retain URL on update", input: notificationChannelRequest{Name: "Renamed"}, requireURL: false},
		{name: "missing name", input: notificationChannelRequest{URL: "logger://"}, requireURL: true, wantError: true},
		{name: "missing create URL", input: notificationChannelRequest{Name: "Missing"}, requireURL: true, wantError: true},
		{name: "unknown service", input: notificationChannelRequest{Name: "Unknown", URL: "unknown://token"}, requireURL: true, wantError: true},
	}
	// Preset templates in frontend/src/utils/notificationTemplates.ts compose URLs
	// of exactly these shapes; keep them accepted by the backend validator.
	for service, templateURL := range map[string]string{
		"discord":  "discord://webhook-token@693853386302554172",
		"slack":    "slack://hook:T000000000-B000000000-XXXXXXXXXXXXXXXXXXXXXXXX@webhook",
		"telegram": "telegram://110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw@telegram?chats=@alerts",
		"smtp":     "smtp://user:password@smtp.example.com:25/?from=alerts@example.com&to=ops@example.com",
		"ntfy":     "ntfy://ntfy.sh/goveto-alerts",
		"gotify":   "gotify://gotify.example.com/AzyoeNS.D4iJLVa",
		"pushover": "pushover://shoutrrr:api-token@user-key/",
		"bark":     "bark://:device-key@api.day.app",
		"generic":  "generic+https://example.com/api/v1/alerts",
	} {
		tests = append(tests, struct {
			name        string
			input       notificationChannelRequest
			requireURL  bool
			wantService string
			wantError   bool
		}{name: "preset " + service, input: notificationChannelRequest{Name: "Preset", URL: templateURL}, requireURL: true, wantService: service})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name, rawURL, service, err := validateNotificationChannelInput(test.input, test.requireURL)
			if (err != nil) != test.wantError {
				t.Fatalf("validate error = %v, wantError %t", err, test.wantError)
			}
			if test.wantError {
				return
			}
			if name != test.input.Name || rawURL != test.input.URL || service != test.wantService {
				t.Fatalf("validate result = (%q, %q, %q), want (%q, %q, %q)", name, rawURL, service, test.input.Name, test.input.URL, test.wantService)
			}
		})
	}
}

func TestNotificationChannelResponseDoesNotExposeEncryptedURL(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	response := newNotificationChannelResponse(&model.NotificationChannel{
		Id: "channel", Name: "Alerts", Service: "slack", UrlEncrypted: "secret-ciphertext",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	})
	if response.MaskedURL != "slack://***" || !response.URLConfigured {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestNotificationChannelEncryptionScopeSeparatesClustersAndChannels(t *testing.T) {
	first := notificationChannelScope("cluster-a", "channel-a")
	for _, other := range []string{
		notificationChannelScope("cluster-b", "channel-a"),
		notificationChannelScope("cluster-a", "channel-b"),
	} {
		if first == other {
			t.Fatalf("scope %q must differ from %q", first, other)
		}
	}
}

func TestDeliverGenericNotificationRejectsPrivateDestination(t *testing.T) {
	target, err := url.Parse("generic+http://127.0.0.1/webhook")
	if err != nil {
		t.Fatal(err)
	}
	if err = deliverGenericNotification(context.Background(), target, "message", "title"); err == nil {
		t.Fatal("private webhook destination was accepted")
	}
}

func TestDeliverGenericNotificationUsesBoundedClient(t *testing.T) {
	httpClient := &http.Client{Transport: notificationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", notificationResponseLimit+1))),
		}, nil
	})}
	target, err := url.Parse("generic+https://8.8.8.8/hook")
	if err != nil {
		t.Fatal(err)
	}
	if err = deliverGenericNotificationWithClient(context.Background(), httpClient, target, "message", "title"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestValidateNotificationURLRejectsUnsafeServicesAndGenericOptions(t *testing.T) {
	for _, rawURL := range []string{
		"googlechat://127.0.0.1/?key=value&token=value",
		"teams://tenant/a/b?host=127.0.0.1",
		"generic+http://example.com/hook",
		"generic+https://example.com/hook?method=DELETE",
		"generic+https://example.com/hook?disabletls=yes",
	} {
		if _, err := validateNotificationURL(rawURL); err == nil {
			t.Fatalf("unsafe notification URL %q was accepted", rawURL)
		}
	}
}

func TestDeliverGenericNotificationRejectsUnsafeHeader(t *testing.T) {
	target, err := url.Parse("generic+https://8.8.8.8/hook?%40Host=internal.example")
	if err != nil {
		t.Fatal(err)
	}
	if err = deliverGenericNotification(context.Background(), target, "message", "title"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("unsafe header error = %v", err)
	}
}

func TestCustomHostHTTPRequestUsesHTTPS(t *testing.T) {
	for _, test := range []struct {
		service string
		rawURL  string
	}{
		{service: "bark", rawURL: "bark://:device-key@api.day.app"},
		{service: "gotify", rawURL: "gotify://gotify.example.com/AzyoeNS.D4iJLVa"},
		{service: "ntfy", rawURL: "ntfy://ntfy.sh/goveto-alerts"},
	} {
		t.Run(test.service, func(t *testing.T) {
			parsed, err := url.Parse(test.rawURL)
			if err != nil {
				t.Fatal(err)
			}
			target, _, _, err := customHostHTTPRequest(test.service, parsed, "message", "title")
			if err != nil {
				t.Fatal(err)
			}
			if target.Scheme != "https" || target.Hostname() != parsed.Hostname() {
				t.Fatalf("target = %s", target)
			}
		})
	}
}

func TestParseMailboxesRejectsHeaderInjection(t *testing.T) {
	if _, err := parseMailboxes("ops@example.com\r\nBcc: attacker@example.com"); err == nil {
		t.Fatal("SMTP recipient header injection was accepted")
	}
}
