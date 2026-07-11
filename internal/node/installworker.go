package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
	staticassets "goveto-edge/static"
)

type InstallWorker struct {
	db    *client.Client
	queue *InstallQueue
}

func NewInstallWorker(db *client.Client, queue *InstallQueue) *InstallWorker {
	return &InstallWorker{db: db, queue: queue}
}
func (w *InstallWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOne(ctx)
		}
	}
}
func (w *InstallWorker) runOne(ctx context.Context) {
	payload, err := w.queue.Claim(ctx)
	if err != nil || payload == nil {
		return
	}

	claimed, err := w.db.Node.Update().
		Where(
			query.Node.Id.Equals(payload.NodeID),
			query.Node.Status.Equals(model.NodeStatusPENDING),
		).
		Set(query.Node.Status.Set(model.NodeStatusINSTALLING)).
		DoMany(ctx)
	if err == nil && claimed == 0 {
		return
	}

	if err == nil {
		err = w.install(ctx, *payload)
	}
	if err != nil {
		_, _ = w.db.Node.Update().
			Where(query.Node.Id.Equals(payload.NodeID)).
			Set(query.Node.Status.Set(model.NodeStatusINSTALL_FAILED)).
			DoMany(ctx)
	}
}
func (w *InstallWorker) install(ctx context.Context, payload InstallPayload) error {
	auth := []ssh.AuthMethod{}
	if payload.SSH.UsesPassword() {
		auth = append(auth, ssh.Password(payload.SSH.Password))
	} else {
		key := []byte(payload.SSH.PrivateKeyPEM)
		var signer ssh.Signer
		var err error
		if payload.SSH.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(payload.SSH.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return err
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}

	config := &ssh.ClientConfig{
		User:            payload.SSH.User,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	}
	connection, err := ssh.Dial("tcp", net.JoinHostPort(payload.SSH.EntryIP, fmt.Sprint(payload.SSH.Port)), config)
	if err != nil {
		return err
	}
	defer connection.Close()

	arch, err := remoteArchitecture(connection)
	if err != nil {
		return err
	}

	binary, err := staticassets.AgentBinary(arch)
	if err != nil {
		return err
	}

	identity, _ := json.Marshal(map[string]string{
		"node_id":           payload.NodeID,
		"communication_key": payload.CommunicationKey,
	})
	unit := `[Unit]
Description=Goveto Edge Agent
After=network-online.target
[Service]
ExecStart=/usr/local/bin/goveto-edge-agent
Restart=always
RestartSec=3
[Install]
WantedBy=multi-user.target
	`

	if err := upload(connection, "/tmp/goveto-edge-agent", binary, 0755); err != nil {
		return err
	}
	if err := upload(connection, "/tmp/goveto-edge-identity.json", identity, 0600); err != nil {
		return err
	}
	if err := upload(connection, "/tmp/goveto-edge-agent.service", []byte(unit), 0644); err != nil {
		return err
	}

	script := `set -eu
sudo install -d -m 0700 /opt/goveto-edge/agent
sudo install -m 0600 /tmp/goveto-edge-identity.json /opt/goveto-edge/agent/identity.json
sudo install -m 0755 /tmp/goveto-edge-agent /usr/local/bin/goveto-edge-agent
sudo install -m 0644 /tmp/goveto-edge-agent.service /etc/systemd/system/goveto-edge-agent.service
sudo systemctl daemon-reload
sudo systemctl enable --now goveto-edge-agent
`

	session, err := connection.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	done := make(chan error, 1)
	go func() { done <- session.Run("sh -c " + shellQuote(script)) }()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return ctx.Err()
	case err := <-done:
		return err
	}
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func remoteArchitecture(connection *ssh.Client) (string, error) {
	session, err := connection.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	output, err := session.Output("uname -m")
	if err != nil {
		return "", err
	}
	return normalizeArchitecture(string(output))
}

func normalizeArchitecture(output string) (string, error) {
	value := strings.TrimSpace(output)
	switch value {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported remote architecture %q", value)
	}
}

func upload(connection *ssh.Client, path string, content []byte, mode uint32) error {
	session, err := connection.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}

	copyDone := make(chan error, 1)
	go func() {
		defer stdin.Close()
		_, err := io.Copy(stdin, bytes.NewReader(content))
		copyDone <- err
	}()

	cmd := fmt.Sprintf("umask 077; cat > %s; chmod %04o %s", shellQuote(path), mode, shellQuote(path))
	if err := session.Run(cmd); err != nil {
		return err
	}
	return <-copyDone
}
