package origingovernance

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
)

type HTTPTransport struct {
	reverseproxy.HTTPTransport
	SiteID               string `json:"site_id"`
	UnhealthyStatus      []int  `json:"unhealthy_status,omitempty"`
	ClientCertificatePEM string `json:"client_certificate_pem,omitempty"`
	ClientPrivateKeyPEM  string `json:"client_private_key_pem,omitempty"`

	temporaryDirectory string
}

func init() { caddy.RegisterModule(HTTPTransport{}) }

func (HTTPTransport) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.reverse_proxy.transport.goveto_http", New: func() caddy.Module { return new(HTTPTransport) }}
}

func (t *HTTPTransport) Provision(ctx caddy.Context) error {
	if (t.ClientCertificatePEM == "") != (t.ClientPrivateKeyPEM == "") {
		return fmt.Errorf("client certificate and private key must be configured together")
	}
	if t.ClientCertificatePEM != "" {
		if _, err := tls.X509KeyPair([]byte(t.ClientCertificatePEM), []byte(t.ClientPrivateKeyPEM)); err != nil {
			return fmt.Errorf("parse origin mTLS key pair: %w", err)
		}
		directory, err := os.MkdirTemp("", "goveto-origin-mtls-")
		if err != nil {
			return err
		}
		t.temporaryDirectory = directory
		certificatePath := filepath.Join(directory, "client.crt")
		privateKeyPath := filepath.Join(directory, "client.key")
		if err = os.WriteFile(certificatePath, []byte(t.ClientCertificatePEM), 0600); err != nil {
			_ = os.RemoveAll(directory)
			return err
		}
		if err = os.WriteFile(privateKeyPath, []byte(t.ClientPrivateKeyPEM), 0600); err != nil {
			_ = os.RemoveAll(directory)
			return err
		}
		if t.TLS == nil {
			t.TLS = new(reverseproxy.TLSConfig)
		}
		t.TLS.ClientCertificateFile = certificatePath
		t.TLS.ClientCertificateKeyFile = privateKeyPath
	}
	if err := t.HTTPTransport.Provision(ctx); err != nil {
		_ = os.RemoveAll(t.temporaryDirectory)
		return err
	}
	return nil
}

func (t *HTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	start := time.Now()
	response, err := t.HTTPTransport.RoundTrip(request)
	if response != nil {
		response.Header.Del("X-Goveto-Origin-Content-Length")
		response.Header.Del("X-Goveto-Origin-Method")
	}
	if replacer, ok := request.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer); ok {
		if address, found := replacer.GetString("goveto.origin.address"); found && address != "" {
			failed := err != nil || responseHasStatus(response, t.UnhealthyStatus)
			observe(t.SiteID, address, time.Since(start), failed)
		}
	}
	return response, err
}

func responseHasStatus(response *http.Response, statuses []int) bool {
	if response == nil {
		return false
	}
	for _, status := range statuses {
		if response.StatusCode == status {
			return true
		}
	}
	return false
}

func (t *HTTPTransport) Cleanup() error {
	err := t.HTTPTransport.Cleanup()
	if removeErr := os.RemoveAll(t.temporaryDirectory); err == nil {
		err = removeErr
	}
	return err
}

var (
	_ caddy.Module       = (*HTTPTransport)(nil)
	_ caddy.Provisioner  = (*HTTPTransport)(nil)
	_ caddy.CleanerUpper = (*HTTPTransport)(nil)
	_ http.RoundTripper  = (*HTTPTransport)(nil)
)
