// Package notify delivers plain-text notifications to Shoutrrr-style
// destinations. It is shared by the notification channel management API (test
// sends) and the alerting engine so both paths enforce the same outbound
// policy, concurrency limits and response bounds.
package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/containrrr/shoutrrr"
	shoutrrrtypes "github.com/containrrr/shoutrrr/pkg/types"

	"goveto-edge/internal/outboundhttp"
)

const (
	// URLLimit bounds stored and submitted Shoutrrr URLs.
	URLLimit = 8192
	// ResponseLimit bounds how much of a destination's response is drained.
	ResponseLimit = 1 << 20
	// SendTimeout bounds one delivery attempt.
	SendTimeout = 10 * time.Second
	// SendConcurrency bounds in-flight deliveries process-wide.
	SendConcurrency = 16
)

// Sentinel errors let callers map results to transport semantics (for example
// HTTP 422 versus 502) without depending on this package's internals.
var (
	ErrInvalidURL     = errors.New("notification URL is invalid")
	ErrDeliveryFailed = errors.New("notification delivery failed")
	ErrSendCongestion = errors.New("notification send queue is saturated")
)

var (
	outboundPolicy = outboundhttp.NewPolicy()
	httpClient     = outboundPolicy.Client("http", "https")
	sendSlots      = make(chan struct{}, SendConcurrency)
)

// ConfigureOutbound replaces the outbound policy used to deliver
// notifications. Pass a policy built with NewPolicyWithAllowlist to permit a
// curated set of private destinations (e.g. an internal gotify or SMTP host);
// the default policy only permits public destinations. Call once at startup
// before serving traffic.
func ConfigureOutbound(policy *outboundhttp.Policy) {
	if policy == nil {
		return
	}
	outboundPolicy = policy
	httpClient = policy.Client("http", "https")
}

// Message is one notification to deliver to a raw Shoutrrr URL. Timeout may
// shorten the limit for direct and Shoutrrr-backed delivery, but cannot extend
// it beyond SendTimeout. Non-positive values use SendTimeout.
type Message struct {
	Title   string
	Body    string
	Timeout time.Duration
}

// Send delivers the message, returning an error wrapping ErrInvalidURL for
// unusable destinations or ErrDeliveryFailed when the destination rejects or
// fails the attempt.
func Send(ctx context.Context, rawURL string, message Message) error {
	if _, err := ValidateURL(rawURL); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidURL, err)
	}
	timeout := boundedSendTimeout(message.Timeout)
	sendCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case sendSlots <- struct{}{}:
		defer func() { <-sendSlots }()
	case <-sendCtx.Done():
		return fmt.Errorf("%w: %w", ErrDeliveryFailed, ErrSendCongestion)
	}
	if err := dispatch(sendCtx, rawURL, message, timeout); err != nil {
		return fmt.Errorf("%w: %w", ErrDeliveryFailed, err)
	}
	return nil
}

func boundedSendTimeout(requested time.Duration) time.Duration {
	if requested <= 0 || requested > SendTimeout {
		return SendTimeout
	}
	return requested
}

func dispatch(ctx context.Context, rawURL string, message Message, timeout time.Duration) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" {
		return errors.New("notification URL is invalid")
	}
	service := ServiceOf(parsed)
	switch service {
	case "bark", "gotify", "ntfy":
		return deliverCustomHost(ctx, service, parsed, message.Body, message.Title)
	case "smtp":
		return deliverSMTP(ctx, parsed, message.Body, message.Title)
	case "generic":
		return deliverGeneric(ctx, parsed, message.Body, message.Title)
	}
	sender, err := shoutrrr.CreateSender(rawURL)
	if err != nil {
		return errors.New("notification URL is invalid")
	}
	sender.Timeout = timeout
	for _, sendErr := range sender.Send(message.Body, &shoutrrrtypes.Params{"title": message.Title}) {
		if sendErr != nil {
			return sendErr
		}
	}
	return nil
}

// ServiceOf extracts the Shoutrrr service name (the scheme before "+").
func ServiceOf(parsed *url.URL) string {
	return strings.ToLower(strings.SplitN(parsed.Scheme, "+", 2)[0])
}

// ChannelScope is the encryption scope for a stored notification channel URL.
// It must stay stable: ciphertexts in the database are bound to this string.
func ChannelScope(clusterID, channelID string) string {
	return "notification-channel:" + clusterID + ":" + channelID
}

// ValidateURL reports whether rawURL is a supported Shoutrrr-style
// destination. Custom-host services (bark/gotify/ntfy/smtp/generic) get direct
// host validation because Shoutrrr's per-service parameter names differ and
// would otherwise reject valid internal HTTP destinations. SaaS services keep
// Shoutrrr validation since their hosts are fixed.
func ValidateURL(rawURL string) (string, error) {
	if len(rawURL) > URLLimit {
		return "", errors.New("url is too long")
	}
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil || parsed.Scheme == "" {
		return "", errors.New("url must be a valid Shoutrrr URL")
	}
	service := ServiceOf(parsed)
	if !isSupportedService(service) {
		return "", errors.New("notification service is not supported")
	}
	if service == "generic" {
		targetScheme := strings.ToLower(strings.TrimPrefix(parsed.Scheme, "generic+"))
		if targetScheme != "http" && targetScheme != "https" && !strings.EqualFold(parsed.Scheme, "generic") {
			return "", errors.New("generic webhook must use HTTP or HTTPS")
		}
		if method := strings.ToUpper(strings.TrimSpace(parsed.Query().Get("method"))); method != "" && method != http.MethodPost {
			return "", errors.New("generic webhook only supports POST")
		}
		for key := range parsed.Query() {
			if !strings.HasPrefix(key, "@") {
				continue
			}
			name := http.CanonicalHeaderKey(strings.TrimPrefix(key, "@"))
			if isForbiddenWebhookHeader(name) {
				return "", fmt.Errorf("generic webhook header %q is not allowed", name)
			}
		}
	}
	if isCustomDeliveryService(service) {
		if err := validateCustomDeliveryURL(parsed, service); err != nil {
			return "", err
		}
		return service, nil
	}
	if _, createErr := shoutrrr.CreateSender(rawURL); createErr != nil {
		return "", errors.New("url must be a valid Shoutrrr URL")
	}
	return service, nil
}

// SafeErrorMessage converts an arbitrary delivery error into a bounded public
// message that never includes the destination URL or credentials.
func SafeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "notification delivery timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "notification delivery canceled"
	}
	if errors.Is(err, ErrInvalidURL) {
		return "notification URL is invalid"
	}
	var statusErr *endpointStatusError
	if errors.As(err, &statusErr) {
		return statusErr.Error()
	}
	if errors.Is(err, errResponseTooLarge) {
		return errResponseTooLarge.Error()
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if errors.Is(urlErr.Err, context.DeadlineExceeded) {
			return "notification delivery timed out"
		}
		return "notification network request failed"
	}
	return "notification delivery failed"
}

func isSupportedService(service string) bool {
	switch service {
	case "bark", "discord", "generic", "gotify", "logger", "ntfy", "pushover", "slack", "smtp", "telegram":
		return true
	default:
		return false
	}
}

func isCustomDeliveryService(service string) bool {
	switch service {
	case "bark", "generic", "gotify", "ntfy", "smtp":
		return true
	default:
		return false
	}
}

func validateCustomDeliveryURL(parsed *url.URL, service string) error {
	if parsed.Host == "" || strings.TrimSpace(parsed.Hostname()) == "" {
		return errors.New("notification host is required")
	}
	scheme := customHostScheme(parsed)
	if service != "smtp" && scheme != "http" && scheme != "https" {
		return errors.New("notification URL must use HTTP or HTTPS")
	}
	// Reuse the delivery builder to confirm required per-service fields (Gotify
	// token, Bark device key, Ntfy topic) are present; the request is discarded.
	if service != "generic" && service != "smtp" {
		if _, _, _, err := customHostHTTPRequest(service, parsed, "", ""); err != nil {
			return err
		}
	}
	return nil
}
