package clusters

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
	"strconv"
	"strings"
	"time"
)

func deliverCustomHostNotification(ctx context.Context, service string, serviceURL *url.URL, message, title string) error {
	if service == "smtp" {
		return deliverSMTPNotification(ctx, serviceURL, message, title)
	}
	target, payload, headers, err := customHostHTTPRequest(service, serviceURL, message, title)
	if err != nil {
		return err
	}
	if err = notificationOutboundPolicy.ValidateURL(ctx, target, "https"); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	for name, value := range headers {
		request.Header[name] = append([]string(nil), value...)
	}
	return sendBoundedNotificationRequest(notificationHTTPClient, request)
}

func customHostHTTPRequest(service string, serviceURL *url.URL, message, title string) (*url.URL, []byte, http.Header, error) {
	query := serviceURL.Query()
	host := serviceURL.Host
	if host == "" {
		return nil, nil, nil, errors.New("notification host is required")
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	target := &url.URL{Scheme: "https", Host: host}
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
		path := strings.TrimSuffix(serviceURL.Path, "/")
		separator := strings.LastIndex(path, "/")
		token := strings.TrimPrefix(path[separator:], "/")
		if token == "" {
			return nil, nil, nil, errors.New("Gotify token is required")
		}
		target.Path = strings.TrimSuffix(path[:separator], "/") + "/message"
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

func sendBoundedNotificationRequest(httpClient *http.Client, request *http.Request) error {
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	read, err := io.Copy(io.Discard, io.LimitReader(response.Body, notificationResponseLimit+1))
	if err != nil {
		return err
	}
	if read > notificationResponseLimit {
		return errors.New("notification response is too large")
	}
	if response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("notification endpoint returned %s", response.Status)
	}
	return nil
}

func deliverSMTPNotification(ctx context.Context, serviceURL *url.URL, message, title string) error {
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
	connection, err := notificationOutboundPolicy.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
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
		if err = client.Auth(smtp.PlainAuth("", serviceURL.User.Username(), password, host)); err != nil {
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
