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
		Set(
			query.Node.Status.Set(model.NodeStatusINSTALLING),
			query.Node.InstallError.SetNull(),
		).
		DoMany(ctx)
	if err == nil && claimed == 0 {
		return
	}

	if err == nil {
		err = w.install(ctx, *payload)
	}
	if err != nil {
		message := installErrorMessage(err)
		_, _ = w.db.Node.Update().
			Where(query.Node.Id.Equals(payload.NodeID)).
			Set(
				query.Node.Status.Set(model.NodeStatusINSTALL_FAILED),
				query.Node.InstallError.Set(message),
			).
			DoMany(ctx)
	}
}
func (w *InstallWorker) install(ctx context.Context, payload InstallPayload) error {
	connection, err := connectSSH(payload.SSH)
	if err != nil {
		return err
	}
	defer connection.Close()

	arch, err := remoteArchitecture(connection)
	if err != nil {
		return fmt.Errorf("detect remote architecture: %w", err)
	}

	binary, err := staticassets.AgentBinary(arch)
	if err != nil {
		return fmt.Errorf("load agent binary for %s: %w", arch, err)
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
		return fmt.Errorf("upload agent binary: %w", err)
	}
	if err := upload(connection, "/tmp/goveto-edge-identity.json", identity, 0600); err != nil {
		return fmt.Errorf("upload node identity: %w", err)
	}
	if err := upload(connection, "/tmp/goveto-edge-agent.service", []byte(unit), 0644); err != nil {
		return fmt.Errorf("upload systemd service: %w", err)
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
		return fmt.Errorf("open installation SSH session: %w", err)
	}
	defer session.Close()

	type commandResult struct {
		output []byte
		err    error
	}
	done := make(chan commandResult, 1)
	go func() {
		output, runErr := session.CombinedOutput("sh -c " + shellQuote(script))
		done <- commandResult{output: output, err: runErr}
	}()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return ctx.Err()
	case result := <-done:
		if result.err != nil {
			return fmt.Errorf(
				"install and start agent service: %w%s",
				result.err,
				commandOutputSuffix(result.output),
			)
		}
		return nil
	}
}

func connectSSH(input SSHInstallInput) (*ssh.Client, error) {
	auth := []ssh.AuthMethod{}
	if input.UsesPassword() {
		auth = append(
			auth,
			ssh.Password(input.Password),
			ssh.KeyboardInteractive(func(
				_ string,
				_ string,
				questions []string,
				echo []bool,
			) ([]string, error) {
				answers := make([]string, len(questions))
				for index := range questions {
					if index >= len(echo) || !echo[index] {
						answers[index] = input.Password
					}
				}
				return answers, nil
			}),
		)
	} else {
		key := []byte(input.PrivateKeyPEM)
		var signer ssh.Signer
		var err error
		if input.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(input.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, fmt.Errorf("parse SSH private key: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}

	config := &ssh.ClientConfig{
		User:            input.User,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	}
	endpoint := net.JoinHostPort(input.EntryIP, fmt.Sprint(input.Port))
	connection, err := ssh.Dial("tcp", endpoint, config)
	if err != nil {
		return nil, fmt.Errorf("connect to %s as %s: %w", endpoint, input.User, err)
	}
	return connection, nil
}

func TestSSHConnection(ctx context.Context, input SSHInstallInput) (string, error) {
	if err := input.Validate(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	connection, err := connectSSH(input)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	architecture, err := remoteArchitecture(connection)
	if err != nil {
		return "", fmt.Errorf("detect remote architecture: %w", err)
	}
	return architecture, nil
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func remoteArchitecture(connection *ssh.Client) (string, error) {
	session, err := connection.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	output, err := session.CombinedOutput("uname -m")
	if err != nil {
		return "", fmt.Errorf("run uname -m: %w%s", err, commandOutputSuffix(output))
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

func commandOutputSuffix(output []byte) string {
	value := strings.TrimSpace(string(output))
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > 2048 {
		value = string(runes[len(runes)-2048:])
	}
	return ": " + value
}

func installErrorMessage(err error) string {
	value := strings.TrimSpace(err.Error())
	runes := []rune(value)
	if len(runes) > 4096 {
		value = string(runes[:4096])
	}
	return value
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
	output, err := session.CombinedOutput(cmd)
	if err != nil {
		return fmt.Errorf("write remote file %s: %w%s", path, err, commandOutputSuffix(output))
	}
	return <-copyDone
}
