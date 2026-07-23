package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
	staticassets "goveto-edge/static"
)

type InstallWorker struct {
	db     *client.Client
	queue  *InstallQueue
	cipher *CredentialCipher
}

func NewInstallWorker(db *client.Client, queue *InstallQueue, cipher *CredentialCipher) *InstallWorker {
	return &InstallWorker{db: db, queue: queue, cipher: cipher}
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
	nodeID := payload.NodeID

	claimed, err := w.db.Node.Update().
		Where(
			query.Node.Id.Equals(nodeID),
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
		payload, err = w.resolveInstallPayload(ctx, payload)
	}
	if err == nil {
		slog.Info(
			"node installation claimed",
			"node_id", payload.NodeID,
			"ssh_host", payload.SSH.EntryIP,
			"ssh_port", payload.SSH.Port,
			"ssh_user", payload.SSH.User,
		)
		err = w.install(ctx, *payload)
	}
	if err != nil {
		message := installErrorMessage(err)
		slog.Error("node installation failed", "node_id", nodeID, "error", message)
		_, _ = w.db.Node.Update().
			Where(query.Node.Id.Equals(nodeID)).
			Set(
				query.Node.Status.Set(model.NodeStatusINSTALL_FAILED),
				query.Node.InstallError.Set(message),
			).
			DoMany(ctx)
		return
	}
	if _, updateErr := w.db.Node.Update().
		Where(query.Node.Id.Equals(nodeID)).
		Set(
			query.Node.Status.Set(model.NodeStatusOFFLINE),
			query.Node.InstallError.SetNull(),
		).
		DoMany(ctx); updateErr != nil {
		slog.Error(
			"node installation completed but status update failed",
			"node_id", nodeID,
			"error", updateErr,
		)
		return
	}
	slog.Info("node installation commands completed", "node_id", nodeID)
}

func (w *InstallWorker) resolveInstallPayload(ctx context.Context, payload *InstallPayload) (*InstallPayload, error) {
	// Continue to accept already-queued legacy payloads during rollout, while
	// all newly enqueued jobs contain only the node ID.
	if payload.CommunicationKey != "" && payload.SSH != nil && payload.SSH.User != "" {
		return payload, nil
	}
	node, err := w.db.Node.FindUnique(ctx, query.Node.Id.Equals(payload.NodeID))
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, fmt.Errorf("node %s was not found", payload.NodeID)
	}
	if node.SshCredentialId == nil || node.SshHost == nil || node.SshPort == nil {
		return nil, errors.New("node SSH installation configuration is missing")
	}
	if *node.SshPort < 1 || *node.SshPort > 65535 {
		return nil, errors.New("node SSH port is invalid")
	}
	communicationCredential, err := w.db.NodeCredential.FindUnique(
		ctx,
		query.NodeCredential.NodeId.Equals(node.Id),
	)
	if err != nil {
		return nil, err
	}
	if communicationCredential == nil {
		return nil, errors.New("node communication credential is missing")
	}
	communicationKey, err := w.cipher.Decrypt(communicationCredential.CommunicationKeyEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt node communication credential: %w", err)
	}
	_, sshInput, err := ResolveSSHInstallInput(ctx, w.db, w.cipher, node.ClusterId, SSHInstallReference{
		EntryIP:      *node.SshHost,
		Port:         uint16(*node.SshPort),
		CredentialID: *node.SshCredentialId,
	})
	if err != nil {
		return nil, err
	}
	return &InstallPayload{
		NodeID:           node.Id,
		CommunicationKey: communicationKey,
		SSH:              &sshInput,
	}, nil
}

func (w *InstallWorker) install(ctx context.Context, payload InstallPayload) error {
	if payload.SSH == nil {
		return errors.New("SSH installation input is missing")
	}
	logger := slog.With(
		"node_id", payload.NodeID,
		"ssh_target", net.JoinHostPort(payload.SSH.EntryIP, fmt.Sprint(payload.SSH.Port)),
	)
	installCtx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	logger.Info("connecting to node over SSH")
	connection, err := connectSSH(installCtx, *payload.SSH)
	if err != nil {
		return err
	}
	defer connection.Close()
	logger.Info("SSH connection established")

	logger.Info("detecting remote architecture", "command", "uname -m")
	arch, err := remoteArchitecture(installCtx, connection)
	if err != nil {
		return fmt.Errorf("detect remote architecture: %w", err)
	}
	logger.Info("remote architecture detected", "architecture", arch)

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

	logger.Info("uploading agent with SCP", "path", "/tmp/goveto-edge-agent", "bytes", len(binary))
	if err := uploadSCP(installCtx, connection, "/tmp/goveto-edge-agent", binary, 0755, 5*time.Minute); err != nil {
		return fmt.Errorf("upload agent binary: %w", err)
	}
	logger.Info("agent upload completed", "path", "/tmp/goveto-edge-agent")
	logger.Info("uploading node identity with SCP", "path", "/tmp/goveto-edge-identity.json", "bytes", len(identity))
	if err := uploadSCP(installCtx, connection, "/tmp/goveto-edge-identity.json", identity, 0600, 30*time.Second); err != nil {
		return fmt.Errorf("upload node identity: %w", err)
	}
	logger.Info("node identity upload completed", "path", "/tmp/goveto-edge-identity.json")
	logger.Info("uploading systemd unit with SCP", "path", "/tmp/goveto-edge-agent.service", "bytes", len(unit))
	if err := uploadSCP(installCtx, connection, "/tmp/goveto-edge-agent.service", []byte(unit), 0644, 30*time.Second); err != nil {
		return fmt.Errorf("upload systemd service: %w", err)
	}
	logger.Info("systemd unit upload completed", "path", "/tmp/goveto-edge-agent.service")

	privileged := privilegedCommandPrefix(payload.SSH.User)
	script := agentInstallScript(privileged)
	logger.Info("running remote installation script", "command", script)

	output, err := runRemoteCommand(
		installCtx,
		connection,
		2*time.Minute,
		"sh -c "+shellQuote(script),
	)
	if err != nil {
		return fmt.Errorf("install and start agent service: %w%s", err, commandOutputSuffix(output))
	}
	if strings.TrimSpace(string(output)) != "" {
		logger.Info("remote installation output", "output", strings.TrimSpace(string(output)))
	}
	logger.Info("remote installation script completed")
	if err := w.collectAndStoreHardwareProfile(installCtx, connection, payload, arch, privileged); err != nil {
		logger.Warn("node hardware benchmark failed", "error", err)
	}
	return nil
}

func agentInstallScript(privileged string) string {
	return fmt.Sprintf(`set -eu
%sinstall -d -m 0700 /opt/goveto-edge/agent
%sinstall -m 0600 /tmp/goveto-edge-identity.json /opt/goveto-edge/agent/identity.json
%sinstall -m 0755 /tmp/goveto-edge-agent /usr/local/bin/goveto-edge-agent
%sinstall -m 0644 /tmp/goveto-edge-agent.service /etc/systemd/system/goveto-edge-agent.service
%ssystemctl daemon-reload
%ssystemctl enable goveto-edge-agent
%ssystemctl restart goveto-edge-agent
%ssystemctl is-active --quiet goveto-edge-agent
`, privileged, privileged, privileged, privileged, privileged, privileged, privileged, privileged)
}

func (w *InstallWorker) collectAndStoreHardwareProfile(
	ctx context.Context,
	connection *ssh.Client,
	payload InstallPayload,
	architecture, privileged string,
) error {
	cacheDirectory := "/opt/goveto-edge/cache"
	config, err := w.db.NodeCacheConfig.FindUnique(
		ctx,
		query.NodeCacheConfig.NodeId.Equals(payload.NodeID),
	)
	if err != nil {
		return fmt.Errorf("load cache directory for hardware benchmark: %w", err)
	}
	if config != nil && strings.TrimSpace(config.CacheDir) != "" {
		cacheDirectory = config.CacheDir
	}

	command := privileged + "/usr/local/bin/goveto-edge-agent benchmark --directory " +
		shellQuote(cacheDirectory) + " --bytes 16777216"
	output, err := runRemoteCommand(ctx, connection, 45*time.Second, command)
	if err != nil {
		return fmt.Errorf("run node hardware benchmark: %w%s", err, commandOutputSuffix(output))
	}
	var profile edgeprotocol.NodeHardwareProfile
	if err := json.Unmarshal(output, &profile); err != nil {
		return fmt.Errorf("decode node hardware benchmark: %w", err)
	}
	if profile.Architecture == "" {
		profile.Architecture = architecture
	}
	if profile.CPUModel == "" {
		profile.CPUModel = architecture
	}
	if profile.MeasuredAt.IsZero() {
		profile.MeasuredAt = time.Now().UTC()
	}

	create := []query.NodeHardwareProfileSetClause{
		query.NodeHardwareProfile.NodeId.Set(payload.NodeID),
		query.NodeHardwareProfile.Architecture.Set(profile.Architecture),
		query.NodeHardwareProfile.CpuModel.Set(profile.CPUModel),
		query.NodeHardwareProfile.MeasuredAt.Set(profile.MeasuredAt),
	}
	update := []query.NodeHardwareProfileSetClause{
		query.NodeHardwareProfile.Architecture.Set(profile.Architecture),
		query.NodeHardwareProfile.CpuModel.Set(profile.CPUModel),
		query.NodeHardwareProfile.MeasuredAt.Set(profile.MeasuredAt),
	}
	if profile.CacheDiskWriteBytesPerSecond != nil {
		value := int64(*profile.CacheDiskWriteBytesPerSecond)
		create = append(create, query.NodeHardwareProfile.CacheDiskWriteBytesPerSecond.Set(value))
		update = append(update, query.NodeHardwareProfile.CacheDiskWriteBytesPerSecond.Set(value))
	} else {
		create = append(create, query.NodeHardwareProfile.CacheDiskWriteBytesPerSecond.SetNull())
		update = append(update, query.NodeHardwareProfile.CacheDiskWriteBytesPerSecond.SetNull())
	}
	if profile.BenchmarkBytes != nil {
		value := int64(*profile.BenchmarkBytes)
		create = append(create, query.NodeHardwareProfile.BenchmarkBytes.Set(value))
		update = append(update, query.NodeHardwareProfile.BenchmarkBytes.Set(value))
	} else {
		create = append(create, query.NodeHardwareProfile.BenchmarkBytes.SetNull())
		update = append(update, query.NodeHardwareProfile.BenchmarkBytes.SetNull())
	}
	if profile.BenchmarkDurationMS != nil {
		value := int(*profile.BenchmarkDurationMS)
		create = append(create, query.NodeHardwareProfile.BenchmarkDurationMs.Set(value))
		update = append(update, query.NodeHardwareProfile.BenchmarkDurationMs.Set(value))
	} else {
		create = append(create, query.NodeHardwareProfile.BenchmarkDurationMs.SetNull())
		update = append(update, query.NodeHardwareProfile.BenchmarkDurationMs.SetNull())
	}
	if _, err := w.db.NodeHardwareProfile.UpsertOne(
		ctx,
		query.NodeHardwareProfile.NodeId.Equals(payload.NodeID),
		create,
		update,
	); err != nil {
		return fmt.Errorf("store node hardware profile: %w", err)
	}
	if profile.DiskBenchmarkError != "" {
		return fmt.Errorf("cache disk benchmark: %s", profile.DiskBenchmarkError)
	}
	return nil
}

func connectSSH(ctx context.Context, input SSHInstallInput) (*ssh.Client, error) {
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
	dialer := net.Dialer{Timeout: 20 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return nil, fmt.Errorf("connect to %s as %s: %w", endpoint, input.User, err)
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	type handshakeResult struct {
		connection ssh.Conn
		channels   <-chan ssh.NewChannel
		requests   <-chan *ssh.Request
		err        error
	}
	done := make(chan handshakeResult, 1)
	go func() {
		connection, channels, requests, handshakeErr := ssh.NewClientConn(raw, endpoint, config)
		done <- handshakeResult{connection, channels, requests, handshakeErr}
	}()
	select {
	case <-handshakeCtx.Done():
		_ = raw.Close()
		return nil, fmt.Errorf("SSH handshake with %s: %w", endpoint, handshakeCtx.Err())
	case result := <-done:
		if result.err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("connect to %s as %s: %w", endpoint, input.User, result.err)
		}
		return ssh.NewClient(result.connection, result.channels, result.requests), nil
	}
}

func TestSSHConnection(ctx context.Context, input SSHInstallInput) (string, error) {
	if err := input.Validate(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	connection, err := connectSSH(ctx, input)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	architecture, err := remoteArchitecture(ctx, connection)
	if err != nil {
		return "", fmt.Errorf("detect remote architecture: %w", err)
	}
	return architecture, nil
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func privilegedCommandPrefix(user string) string {
	if strings.TrimSpace(user) == "root" {
		return ""
	}
	return "sudo "
}

func remoteArchitecture(ctx context.Context, connection *ssh.Client) (string, error) {
	output, err := runRemoteCommand(ctx, connection, 30*time.Second, "uname -m")
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

func uploadSCP(
	ctx context.Context,
	connection *ssh.Client,
	remotePath string,
	content []byte,
	mode uint32,
	timeout time.Duration,
) error {
	session, err := connection.NewSession()
	if err != nil {
		return fmt.Errorf("open SCP session: %w", err)
	}
	defer session.Close()
	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("open SCP stdin: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open SCP stdout: %w", err)
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr
	targetDir := path.Dir(remotePath)
	filename := path.Base(remotePath)
	if err := session.Start("scp -t -- " + shellQuote(targetDir)); err != nil {
		return fmt.Errorf("start remote SCP: %w", err)
	}

	result := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(stdout)
		if err := readSCPAck(reader); err != nil {
			result <- err
			return
		}
		if _, err := fmt.Fprintf(stdin, "C%04o %d %s\n", mode, len(content), filename); err != nil {
			result <- err
			return
		}
		if err := readSCPAck(reader); err != nil {
			result <- err
			return
		}
		if _, err := stdin.Write(content); err != nil {
			result <- err
			return
		}
		if _, err := stdin.Write([]byte{0}); err != nil {
			result <- err
			return
		}
		if err := readSCPAck(reader); err != nil {
			result <- err
			return
		}
		if err := stdin.Close(); err != nil {
			result <- err
			return
		}
		result <- session.Wait()
	}()
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-operationCtx.Done():
		_ = session.Close()
		return fmt.Errorf("SCP upload %s timed out: %w", remotePath, operationCtx.Err())
	case err := <-result:
		if err != nil {
			return fmt.Errorf("SCP upload %s: %w%s", remotePath, err, commandOutputSuffix(stderr.Bytes()))
		}
		return nil
	}
}

func readSCPAck(reader *bufio.Reader) error {
	code, err := reader.ReadByte()
	if err != nil {
		return fmt.Errorf("read SCP acknowledgement: %w", err)
	}
	switch code {
	case 0:
		return nil
	case 1, 2:
		message, readErr := reader.ReadString('\n')
		if readErr != nil {
			return fmt.Errorf("remote SCP error %d", code)
		}
		return fmt.Errorf("remote SCP error: %s", strings.TrimSpace(message))
	default:
		return fmt.Errorf("unexpected SCP acknowledgement %d", code)
	}
}

func runRemoteCommand(
	ctx context.Context,
	connection *ssh.Client,
	timeout time.Duration,
	command string,
) ([]byte, error) {
	session, err := connection.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open SSH session: %w", err)
	}
	defer session.Close()
	type commandResult struct {
		output []byte
		err    error
	}
	result := make(chan commandResult, 1)
	go func() {
		output, runErr := session.CombinedOutput(command)
		result <- commandResult{output: output, err: runErr}
	}()
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-operationCtx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		return nil, fmt.Errorf("remote command timed out: %w", operationCtx.Err())
	case value := <-result:
		return value.output, value.err
	}
}
