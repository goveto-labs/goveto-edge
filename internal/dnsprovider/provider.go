// Package dnsprovider contains DNS vendor adapters used by the control plane.
//
// Providers register a Descriptor in a package registry from their init
// functions. Callers only interact with the Provider interface, the optional
// discovery capability interfaces, and the package-level constructors, so a
// new vendor can be added without modifying this file or any call site.
package dnsprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

type Record struct {
	ID       string
	Hostname string
	Type     model.DNSRecordType
	Value    string
	Line     string
	TTL      int
	Proxied  bool
}

// Capabilities describes what a provider implementation supports. Callers
// must treat unknown capabilities as false so new fields stay backward safe.
type Capabilities struct {
	// Lines indicates regional line selection support (for example ISP lines).
	Lines bool
	// Proxied indicates the provider can route records through its own network
	// (Cloudflare orange cloud).
	Proxied bool
}

// Provider is the stable SPI every DNS vendor adapter implements.
type Provider interface {
	Upsert(context.Context, Record) (string, error)
	Delete(context.Context, Record) error
	ListRecords(context.Context, string) ([]Record, error)
	// Capabilities reports the feature set of this provider instance.
	Capabilities() Capabilities
}

// SupportsLines reports whether the provider exposes regional lines.
func SupportsLines(provider Provider) bool {
	return provider.Capabilities().Lines
}

// DomainLister is the optional capability to enumerate the zones available to
// a set of credentials. It does not require a configured zone.
type DomainLister interface {
	ListDomains(ctx context.Context) ([]Domain, error)
}

// LineLister is the optional capability to enumerate the regional lines of a
// configured zone. Providers without line support fall back to a single
// default line.
type LineLister interface {
	ListLines(ctx context.Context) ([]Line, error)
}

// CredentialField describes one credential input required by a provider.
type CredentialField struct {
	// Name is the stable identifier used in the credentials JSON payload.
	Name string `json:"name"`
	// Label is the human-readable input label.
	Label string `json:"label"`
	// Required marks inputs that must be non-empty.
	Required bool `json:"required"`
	// Secret marks inputs that must be masked in responses and logs.
	Secret bool `json:"secret"`
}

// Config carries every input a provider factory needs.
type Config struct {
	// Zone is the lower-cased DNS zone. It is empty for discovery-only use.
	Zone string
	// ZoneID is the provider-specific zone identifier when applicable.
	ZoneID string
	// Credentials are the decoded, provider-specific credentials.
	Credentials Credentials
	// Client is never nil when the factory is invoked through a constructor.
	Client *http.Client
}

// Descriptor registers a DNS provider implementation.
type Descriptor struct {
	// Name is the human-readable provider label used in errors.
	Name string
	// CredentialFields describes the credential inputs in input order.
	CredentialFields []CredentialField
	// RequiresZoneID marks providers that need a provider-specific zone
	// identifier in addition to the zone name for record operations.
	RequiresZoneID bool
	// Capabilities is the static feature set of this provider type.
	Capabilities Capabilities
	// Factory builds a provider instance. Factories must not require a zone
	// themselves because discovery constructs providers without one; zone
	// requirements belong in Validate.
	Factory func(Config) (Provider, error)
	// Validate rejects configurations that cannot serve record operations.
	// It is only called for record-operation construction, not discovery.
	Validate func(Config) error
	// ValidateCredentials reports whether decoded credentials are complete.
	ValidateCredentials func(Credentials) error
	// ParseCredentials sanitizes a raw credential map from an API request
	// into the stored credentials payload.
	ParseCredentials func(map[string]string) (Credentials, error)
}

var (
	registryMu sync.RWMutex
	registry   = map[model.DNSProviderType]*Descriptor{}
)

// Register adds a provider implementation to the registry. It is intended for
// package init functions of adapters inside dnsprovider. Registering the same
// provider type twice is a programming error and panics instead of silently
// replacing the previous descriptor.
func Register(kind model.DNSProviderType, descriptor *Descriptor) {
	if descriptor == nil || descriptor.Factory == nil {
		panic(fmt.Sprintf("dnsprovider: incomplete descriptor for %q", kind))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if registry[kind] != nil {
		panic(fmt.Sprintf("dnsprovider: provider %q registered twice", kind))
	}
	registry[kind] = descriptor
}

// DescriptorFor returns the registered descriptor for a provider type.
func DescriptorFor(kind model.DNSProviderType) (*Descriptor, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	descriptor := registry[kind]
	if descriptor == nil {
		return nil, fmt.Errorf("unsupported DNS provider %q", kind)
	}
	return descriptor, nil
}

type APIError struct {
	Provider string
	Status   string
	Code     string
	Message  string
}

func (e *APIError) Error() string {
	label := e.Provider + " DNS API"
	if e.Code != "" {
		label += " " + e.Code
	} else if e.Status != "" {
		label += " " + e.Status
	}
	if e.Message == "" {
		return label
	}
	return label + ": " + e.Message
}

// Sentinel errors classifying provider failures. Adapters wrap these around
// their APIError so callers can react with errors.Is instead of matching
// vendor error strings.
var (
	// ErrRecordExists reports a conflicting record already present at the provider.
	ErrRecordExists = errors.New("DNS record already exists")
	// ErrRecordNotFound reports that the provider record no longer exists.
	ErrRecordNotFound = errors.New("DNS record not found")
	// ErrInvalidCredentials reports rejected credentials; retrying with the
	// same credentials cannot succeed.
	ErrInvalidCredentials = errors.New("DNS provider credentials were rejected")
	// ErrRateLimited reports provider throttling.
	ErrRateLimited = errors.New("DNS provider rate limit exceeded")
)

// RateLimitedError carries the provider's retry hint when one is available.
type RateLimitedError struct {
	Provider   string
	RetryAfter time.Duration
	// Err is the underlying provider failure, typically an *APIError.
	Err error
}

func (e *RateLimitedError) Error() string {
	message := e.Provider + " DNS API rate limit exceeded"
	if e.RetryAfter > 0 {
		message += fmt.Sprintf(", retry after %s", e.RetryAfter)
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

// Unwrap exposes both the rate limit sentinel and the underlying provider
// error, matching how the other sentinels wrap their APIError.
func (e *RateLimitedError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrRateLimited}
	}
	return []error{ErrRateLimited, e.Err}
}

func IsRecordAlreadyExists(err error) bool { return errors.Is(err, ErrRecordExists) }

func IsRecordNotFound(err error) bool { return errors.Is(err, ErrRecordNotFound) }

// IsInvalidCredentials reports whether err was caused by rejected provider
// credentials. Such failures are permanent until the credentials change.
func IsInvalidCredentials(err error) bool { return errors.Is(err, ErrInvalidCredentials) }

// IsRateLimited reports whether err was caused by provider throttling.
func IsRateLimited(err error) bool { return errors.Is(err, ErrRateLimited) }

// RateLimitRetryAfter returns the provider's retry hint for a rate limited
// error. It returns 0 when err is nil, not rate limited, or the provider did
// not provide a hint, in which case the caller should use its default
// backoff.
func RateLimitRetryAfter(err error) time.Duration {
	var rateLimited *RateLimitedError
	if errors.As(err, &rateLimited) {
		return rateLimited.RetryAfter
	}
	return 0
}

// maxRetryAfter bounds the retry hint a provider can impose through a
// Retry-After header, so a bogus far-future value cannot stall retries.
const maxRetryAfter = 10 * time.Minute

// parseRetryAfter parses an HTTP Retry-After header value, which is either a
// delay in seconds or an HTTP date. The result is clamped to maxRetryAfter.
// It returns 0 for missing or invalid input.
func parseRetryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		return min(time.Duration(seconds)*time.Second, maxRetryAfter)
	}
	if date, err := http.ParseTime(header); err == nil {
		if delay := date.Sub(now); delay > 0 {
			return min(delay, maxRetryAfter)
		}
	}
	return 0
}

func CanonicalValue(kind model.DNSRecordType, value string) string {
	value = strings.TrimSpace(value)
	if kind == model.DNSRecordTypeA || kind == model.DNSRecordTypeAAAA {
		if ip := net.ParseIP(value); ip != nil {
			return ip.String()
		}
	}
	return value
}

// ExpectedTTL returns the TTL a provider stores for the record. Proxied
// records always use the provider's automatic TTL, so callers comparing a
// desired record against a listed remote record must use this value.
func ExpectedTTL(record Record) int {
	if record.Proxied {
		return 1
	}
	return record.TTL
}

type Credentials struct {
	AccessKeyID     string `json:"access_key_id,omitempty"`
	AccessKeySecret string `json:"access_key_secret,omitempty"`
	APIToken        string `json:"api_token,omitempty"`
}

type Domain struct {
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
}

type Line struct {
	Name       string `json:"name"`
	Code       string `json:"code"`
	ParentCode string `json:"parent_code,omitempty"`
	SortOrder  int    `json:"sort_order"`
}

// sharedClient is used when a caller passes a nil *http.Client. Unlike
// http.DefaultClient it bounds request time so callers cannot hang forever on
// a stalled vendor API. Tests keep passing their own client and are unaffected.
var sharedClient = &http.Client{Timeout: 20 * time.Second}

func httpClientOrDefault(client *http.Client) *http.Client {
	if client == nil {
		return sharedClient
	}
	return client
}

// decodeCredentials decodes and validates the credentials payload for kind.
func decodeCredentials(kind model.DNSProviderType, raw []byte) (Credentials, *Descriptor, error) {
	descriptor, err := DescriptorFor(kind)
	if err != nil {
		return Credentials{}, nil, err
	}
	var value Credentials
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, descriptor, fmt.Errorf("decode DNS credentials: %w", err)
	}
	if descriptor.ValidateCredentials != nil {
		if err := descriptor.ValidateCredentials(value); err != nil {
			return value, descriptor, err
		}
	}
	return value, descriptor, nil
}

// ProbeResult reports what a set of credentials can access, determined
// without side effects.
type ProbeResult struct {
	// Provider is the human-readable provider label.
	Provider string `json:"provider"`
	// Zones lists the zones visible to the credentials.
	Zones []Domain `json:"zones"`
}

// Probe validates credentials and enumerates the zones they can manage. It
// performs no writes, so it is safe to call before saving a configuration.
// Credentials rejected by the provider API are reported wrapped in
// ErrInvalidCredentials; malformed or incomplete credentials fail local
// validation before any request is made.
func Probe(ctx context.Context, kind model.DNSProviderType, raw []byte, client *http.Client) (ProbeResult, error) {
	credentials, descriptor, err := decodeCredentials(kind, raw)
	if err != nil {
		return ProbeResult{}, err
	}
	provider, err := descriptor.Factory(Config{Credentials: credentials, Client: httpClientOrDefault(client)})
	if err != nil {
		return ProbeResult{}, err
	}
	lister, ok := provider.(DomainLister)
	if !ok {
		return ProbeResult{}, fmt.Errorf("%s DNS does not support credential probing", descriptor.Name)
	}
	zones, err := lister.ListDomains(ctx)
	if err != nil {
		return ProbeResult{}, err
	}
	return ProbeResult{Provider: descriptor.Name, Zones: zones}, nil
}

func ListDomains(ctx context.Context, kind model.DNSProviderType, raw []byte, client *http.Client) ([]Domain, error) {
	credentials, descriptor, err := decodeCredentials(kind, raw)
	if err != nil {
		return nil, err
	}
	provider, err := descriptor.Factory(Config{Credentials: credentials, Client: httpClientOrDefault(client)})
	if err != nil {
		return nil, err
	}
	lister, ok := provider.(DomainLister)
	if !ok {
		return nil, fmt.Errorf("%s DNS does not support domain discovery", descriptor.Name)
	}
	return lister.ListDomains(ctx)
}

func ListLines(ctx context.Context, kind model.DNSProviderType, zone, zoneID string, raw []byte, client *http.Client) ([]Line, error) {
	credentials, descriptor, err := decodeCredentials(kind, raw)
	if err != nil {
		return nil, err
	}
	provider, err := descriptor.Factory(Config{
		Zone:        strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), "."),
		ZoneID:      zoneID,
		Credentials: credentials,
		Client:      httpClientOrDefault(client),
	})
	if err != nil {
		return nil, err
	}
	if liner, ok := provider.(LineLister); ok {
		return liner.ListLines(ctx)
	}
	return []Line{{Name: "Default", Code: "default", SortOrder: 0}}, nil
}

func New(kind model.DNSProviderType, zone, zoneID string, raw []byte, client *http.Client) (Provider, error) {
	credentials, descriptor, err := decodeCredentials(kind, raw)
	if err != nil {
		return nil, err
	}
	zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	if zone == "" {
		return nil, fmt.Errorf("DNS zone is required")
	}
	config := Config{Zone: zone, ZoneID: zoneID, Credentials: credentials, Client: httpClientOrDefault(client)}
	if descriptor.Validate != nil {
		if err := descriptor.Validate(config); err != nil {
			return nil, err
		}
	}
	return descriptor.Factory(config)
}

func RelativeName(hostname, zone string) (string, error) {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	if hostname == zone {
		return "@", nil
	}
	suffix := "." + zone
	if !strings.HasSuffix(hostname, suffix) {
		return "", fmt.Errorf("hostname %q is outside DNS zone %q", hostname, zone)
	}
	return strings.TrimSuffix(hostname, suffix), nil
}
