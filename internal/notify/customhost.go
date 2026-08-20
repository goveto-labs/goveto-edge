package notify

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

var errResponseTooLarge = errors.New("notification response is too large")

type endpointStatusError struct {
	status string
}

func (e *endpointStatusError) Error() string {
	return "notification endpoint returned " + e.status
}

func deliverCustomHost(ctx context.Context, service string, serviceURL *url.URL, message, title string) error {
	if service == "smtp" {
		return deliverSMTP(ctx, serviceURL, message, title)
	}
	target, payload, headers, err := customHostHTTPRequest(service, serviceURL, message, title)
	if err != nil {
		return err
	}
	if err = outboundPolicy.ValidateURL(ctx, target, "http", "https"); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	for name, value := range headers {
		request.Header[name] = append([]string(nil), value...)
	}
	return sendBoundedRequest(httpClient, request)
}

func customHostHTTPRequest(service string, serviceURL *url.URL, message, title string) (*url.URL, []byte, http.Header, error) {
	query := serviceURL.Query()
	host := serviceURL.Host
	if host == "" {
		return nil, nil, nil, errors.New("notification host is required")
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	target := &url.URL{Scheme: customHostScheme(serviceURL), Host: host}
	var payload any
	switch service {
	case "bark":
		deviceKey, _ := serviceURL.User.Password()
		if deviceKey == "" {
			return nil, nil, nil, errors.New("Bark device key is required")
		}
		path := strings.TrimSuffix(serviceURL.Path, "/")
		target.Path = path + "/push"
		payload = map[string]any{
			"body": message, "title": title, "device_key": deviceKey,
			"category": query.Get("category"), "copy": query.Get("copy"),
			"sound": query.Get("sound"), "group": query.Get("group"),
			"icon": query.Get("icon"),
		}
	case "gotify":
		pathValue := strings.Trim(serviceURL.Path, "/")
		token := pathValue
		base := ""
		if index := strings.LastIndex(pathValue, "/"); index >= 0 {
			token = pathValue[index+1:]
			base = pathValue[:index]
		}
		if token == "" {
			return nil, nil, nil, errors.New("Gotify token is required")
		}
		target.Path = path.Join("/", base, "message")
		target.RawQuery = url.Values{"token": {token}}.Encode()
		priority, _ := strconv.Atoi(query.Get("priority"))
		payload = map[string]any{"message": message, "title": title, "priority": priority}
	case "ntfy":
		topic := strings.TrimPrefix(serviceURL.Path, "/")
		if topic == "" {
			return nil, nil, nil, errors.New("Ntfy topic is required")
		}
		target.Path = "/" + topic
		headers.Set("Content-Type", "text/plain; charset=utf-8")
		headers.Set("Title", title)
		if serviceURL.User != nil {
			password, _ := serviceURL.User.Password()
			headers.Set("Authorization", "Basic "+basicAuth(serviceURL.User.Username(), password))
		}
		for key, name := range map[string]string{
			"tags": "Tags", "priority": "Priority", "actions": "Actions", "click": "Click",
			"attach": "Attach", "filename": "Filename", "delay": "Delay", "email": "Email", "icon": "X-Icon",
		} {
			if value := query.Get(key); value != "" {
				headers.Set(name, value)
			}
		}
		return target, []byte(message), headers, nil
	default:
		return nil, nil, nil, fmt.Errorf("unsupported custom-host notification service %q", service)
	}
	encoded, err := json.Marshal(payload)
	return target, encoded, headers, err
}

// customHostScheme derives the HTTP scheme for a custom-host service from the
// Shoutrrr-style URL. A service+http scheme or disabletls flag selects HTTP;
// everything else defaults to HTTPS. The destination is still SSRF-validated,
// so private hosts require the operator allowlist regardless of scheme.
func customHostScheme(serviceURL *url.URL) string {
	if disable := strings.ToLower(strings.TrimSpace(serviceURL.Query().Get("disabletls"))); disable == "yes" || disable == "true" {
		return "http"
	}
	if _, suffix, ok := strings.Cut(serviceURL.Scheme, "+"); ok {
		if strings.EqualFold(suffix, "http") {
			return "http"
		}
		if strings.EqualFold(suffix, "https") {
			return "https"
		}
	}
	return "https"
}

func sendBoundedRequest(client *http.Client, request *http.Request) error {
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	read, err := io.Copy(io.Discard, io.LimitReader(response.Body, ResponseLimit+1))
	if err != nil {
		return err
	}
	if read > ResponseLimit {
		return errResponseTooLarge
	}
	if response.StatusCode >= http.StatusMultipleChoices {
		return &endpointStatusError{status: response.Status}
	}
	return nil
}

func deliverGeneric(ctx context.Context, serviceURL *url.URL, message, title string) error {
	return deliverGenericWithClient(ctx, httpClient, serviceURL, message, title)
}

func deliverGenericWithClient(ctx context.Context, client *http.Client, serviceURL *url.URL, message, title string) error {
	target := *serviceURL
	switch {
	case strings.HasPrefix(strings.ToLower(target.Scheme), "generic+"):
		target.Scheme = target.Scheme[len("generic+"):]
	case strings.EqualFold(target.Scheme, "generic"):
		target.Scheme = "https"
	default:
		return errors.New("invalid generic webhook scheme")
	}
	query := target.Query()
	method := strings.ToUpper(strings.TrimSpace(query.Get("method")))
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodPost {
		return errors.New("generic webhooks only support POST")
	}
	contentType := strings.TrimSpace(query.Get("contenttype"))
	if contentType == "" {
		contentType = "application/json"
	}
	body := []byte(message)
	if strings.EqualFold(query.Get("template"), "json") {
		messageKey := strings.TrimSpace(query.Get("messagekey"))
		if messageKey == "" {
			messageKey = "message"
		}
		titleKey := strings.TrimSpace(query.Get("titlekey"))
		if titleKey == "" {
			titleKey = "title"
		}
		payload := map[string]string{messageKey: message, titleKey: title}
		for key, values := range query {
			if strings.HasPrefix(key, "$") && len(values) > 0 {
				payload[strings.TrimPrefix(key, "$")] = values[0]
			}
		}
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	} else if template := strings.TrimSpace(query.Get("template")); template != "" {
		return fmt.Errorf("generic webhook template %q is not supported", template)
	}
	for key := range query {
		switch {
		case strings.HasPrefix(key, "@"), strings.HasPrefix(key, "$"), isGenericConfigKey(key):
			query.Del(key)
		case strings.HasPrefix(key, "__"):
			values := query[key]
			query.Del(key)
			query[strings.TrimPrefix(key, "__")] = values
		}
	}
	target.RawQuery = query.Encode()
	if err := outboundPolicy.ValidateURL(ctx, &target, "http", "https"); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", contentType)
	for key, values := range serviceURL.Query() {
		if strings.HasPrefix(key, "@") && len(values) > 0 {
			name := http.CanonicalHeaderKey(strings.TrimPrefix(key, "@"))
			if isForbiddenWebhookHeader(name) {
				return fmt.Errorf("generic webhook header %q is not allowed", name)
			}
			request.Header.Set(name, values[0])
		}
	}
	return sendBoundedRequest(client, request)
}

func isForbiddenWebhookHeader(name string) bool {
	switch name {
	case "Connection", "Content-Length", "Host", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

func isGenericConfigKey(key string) bool {
	switch strings.ToLower(key) {
	case "contenttype", "disabletls", "messagekey", "method", "template", "title", "titlekey":
		return true
	default:
		return false
	}
}

func deliverSMTP(ctx context.Context, serviceURL *url.URL, message, title string) error {
	host := serviceURL.Hostname()
	if host == "" {
		return errors.New("SMTP host is required")
	}
	port := serviceURL.Port()
	if port == "" {
		port = "25"
	}
	query := serviceURL.Query()
	from, err := parseMailbox(firstQueryValue(query, "from", "fromaddress"))
	if err != nil {
		return fmt.Errorf("invalid SMTP from address: %w", err)
	}
	recipients, err := parseMailboxes(firstQueryValue(query, "to", "toaddresses"))
	if err != nil || len(recipients) == 0 {
		return errors.New("SMTP from and to addresses are required")
	}
	connection, err := outboundPolicy.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	defer connection.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = connection.SetDeadline(time.Now()) })
	defer stopCancel()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	}
	implicitTLS := port == "465" || strings.EqualFold(query.Get("encryption"), "implicitTLS")
	if implicitTLS {
		connection = tls.Client(connection, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host})
		if err = connection.(*tls.Conn).HandshakeContext(ctx); err != nil {
			return err
		}
	}
	client, err := smtp.NewClient(connection, host)
	if err != nil {
		return err
	}
	defer client.Close()
	startTLS := !strings.EqualFold(query.Get("starttls"), "no") && !strings.EqualFold(query.Get("usestarttls"), "no")
	if startTLS && !implicitTLS {
		if supported, _ := client.Extension("STARTTLS"); supported {
			if err = client.StartTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}); err != nil {
				return err
			}
		}
	}
	if serviceURL.User != nil && serviceURL.User.Username() != "" {
		password, _ := serviceURL.User.Password()
		if err = client.Auth(smtpPlainAuth{username: serviceURL.User.Username(), password: password}); err != nil {
			return err
		}
	}
	if err = client.Mail(from); err != nil {
		return err
	}
	for _, recipient := range recipients {
		if err = client.Rcpt(recipient); err != nil {
			return err
		}
	}
	wc, err := client.Data()
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(wc)
	_, err = fmt.Fprintf(writer, "From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", from, strings.Join(recipients, ", "), title, message)
	if flushErr := writer.Flush(); err == nil {
		err = flushErr
	}
	if closeErr := wc.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return client.Quit()
}

// smtpPlainAuth performs SASL PLAIN authentication. Unlike smtp.PlainAuth it
// does not require a TLS connection, so SMTP servers that cannot upgrade via
// STARTTLS remain usable for internal relays. STARTTLS is still attempted when
// the server supports it, and the destination must clear SSRF validation (public
// or on the operator allowlist) before credentials are sent.
type smtpPlainAuth struct {
	identity string
	username string
	password string
}

func (a smtpPlainAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte(a.identity + "\x00" + a.username + "\x00" + a.password), nil
}

func (smtpPlainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected SMTP server challenge")
	}
	return nil, nil
}

func firstQueryValue(values url.Values, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func parseMailbox(value string) (string, error) {
	address, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	return address.Address, nil
}

func parseMailboxes(value string) ([]string, error) {
	addresses, err := mail.ParseAddressList(strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	result := make([]string, len(addresses))
	for index, address := range addresses {
		result[index] = address.Address
	}
	return result, nil
}

func basicAuth(username, password string) string {
	request := &http.Request{Header: make(http.Header)}
	request.SetBasicAuth(username, password)
	return strings.TrimPrefix(request.Header.Get("Authorization"), "Basic ")
}
