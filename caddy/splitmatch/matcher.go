package splitmatch

import (
	"crypto/sha256"
	"encoding/binary"
	"net"
	"net/http"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

type Matcher struct {
	HeaderName string `json:"header_name,omitempty"`
	CookieName string `json:"cookie_name,omitempty"`
	Value      string `json:"value,omitempty"`
	Percentage int    `json:"percentage,omitempty"`
	Salt       string `json:"salt,omitempty"`
}

func init() { caddy.RegisterModule(Matcher{}) }

func (Matcher) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.matchers.goveto_split", New: func() caddy.Module { return new(Matcher) }}
}

func (m Matcher) Match(r *http.Request) bool {
	key := ""
	if m.HeaderName != "" {
		key = r.Header.Get(m.HeaderName)
		if key == "" || m.Value != "" && key != m.Value {
			return false
		}
	}
	if m.CookieName != "" {
		cookie, err := r.Cookie(m.CookieName)
		if err != nil || m.Value != "" && cookie.Value != m.Value {
			return false
		}
		key = cookie.Value
	}
	if m.Percentage <= 0 {
		return m.HeaderName != "" || m.CookieName != ""
	}
	if m.Percentage >= 100 {
		return true
	}
	if key == "" {
		key = clientIdentity(r)
	}
	digest := sha256.Sum256([]byte(m.Salt + "\x00" + key))
	return int(binary.BigEndian.Uint32(digest[:4])%100) < m.Percentage
}

func clientIdentity(r *http.Request) string {
	address := r.RemoteAddr
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		address = forwarded
	} else if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	return address + "\x00" + r.UserAgent()
}

var _ caddyhttp.RequestMatcher = (*Matcher)(nil)
