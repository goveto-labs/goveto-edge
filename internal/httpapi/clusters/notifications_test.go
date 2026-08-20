package clusters

import (
	"testing"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

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
