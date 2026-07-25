package edgeagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"goveto-edge/internal/edgeprotocol"
)

var errCredentialRotated = errors.New("agent credential rotated")

type channelClient struct {
	identityPath string
	identity     Identity
	configs      *ConfigManager
	nodeConfigs  *NodeConfigStore
	logs         *LogQueue
}

func (c *channelClient) Run(ctx context.Context) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.runSession(ctx)
		if errors.Is(err, errCredentialRotated) {
			attempt = 0
		} else if err != nil && ctx.Err() == nil {
			slog.Warn("agent management channel disconnected", "error", err)
		}
		delay := reconnectDelay(attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *channelClient) runSession(ctx context.Context) error {
	tlsConfig, err := c.tlsConfig()
	if err != nil {
		return err
	}
	connection, err := grpc.NewClient(
		c.identity.GatewayAddress,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithDefaultCallOptions(
			grpc.ForceCodec(edgeprotocol.JSONCodec{}),
			grpc.MaxCallRecvMsgSize(32<<20),
			grpc.MaxCallSendMsgSize(32<<20),
		),
	)
	if err != nil {
		return err
	}
	defer connection.Close()
	stream, err := edgeprotocol.NewManagementClient(connection).Connect(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&edgeprotocol.ClientMessage{Hello: &edgeprotocol.AgentHello{
		NodeID: c.identity.NodeID, CacheConfig: c.nodeConfigs.Get(), SiteVersions: c.configs.SiteVersions(),
	}}); err != nil {
		return err
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	outbound := make(chan *edgeprotocol.ClientMessage, 32)
	writeErrors := make(chan error, 1)
	go func() {
		for {
			select {
			case <-sessionCtx.Done():
				return
			case message := <-outbound:
				if err := stream.Send(message); err != nil {
					writeErrors <- err
					return
				}
			}
		}
	}()

	tasks := make(chan edgeprotocol.AgentTask, 16)
	go c.runTasks(sessionCtx, tasks, outbound)
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	logsWake := time.NewTicker(2 * time.Second)
	defer logsWake.Stop()
	logsOutstanding := false
	var pendingPrivateKey ed25519.PrivateKey
	received := make(chan struct {
		message *edgeprotocol.ServerMessage
		err     error
	}, 1)
	go func() {
		for {
			message, recvErr := stream.Recv()
			select {
			case received <- struct {
				message *edgeprotocol.ServerMessage
				err     error
			}{message, recvErr}:
			case <-sessionCtx.Done():
				return
			}
			if recvErr != nil {
				return
			}
		}
	}()

	for {
		if !logsOutstanding {
			batch, batchErr := c.logs.Batch(1000)
			if batchErr != nil {
				return batchErr
			}
			if len(batch) > 0 {
				logsOutstanding = true
				if err := sendClientMessage(sessionCtx, outbound, &edgeprotocol.ClientMessage{Logs: &edgeprotocol.AgentLogBatch{
					Through: batch[len(batch)-1].ID, Records: batch,
				}}); err != nil {
					return err
				}
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-writeErrors:
			return err
		case <-heartbeat.C:
			if err := sendClientMessage(sessionCtx, outbound, &edgeprotocol.ClientMessage{Heartbeat: &edgeprotocol.AgentHeartbeat{
				CacheConfig: c.nodeConfigs.Get(), SiteVersions: c.configs.SiteVersions(),
			}}); err != nil {
				return err
			}
		case <-logsWake.C:
		case response := <-received:
			if response.err != nil {
				return response.err
			}
			message := response.message
			switch {
			case message.Task != nil:
				select {
				case tasks <- *message.Task:
				case <-ctx.Done():
					return ctx.Err()
				}
			case message.LogsAckThrough != nil:
				if err := c.logs.Ack(*message.LogsAckThrough); err != nil {
					return err
				}
				logsOutstanding = false
			case message.RotateCredential:
				if pendingPrivateKey == nil {
					request, privateKey, err := c.prepareCredentialRequest()
					if err != nil {
						return err
					}
					pendingPrivateKey = privateKey
					if err := sendClientMessage(sessionCtx, outbound, &edgeprotocol.ClientMessage{CredentialRequest: request}); err != nil {
						return err
					}
				}
			case message.Credential != nil:
				if pendingPrivateKey == nil {
					return errors.New("received an unsolicited agent credential")
				}
				privateDER, err := x509.MarshalPKCS8PrivateKey(pendingPrivateKey)
				if err != nil {
					return err
				}
				updated := c.identity
				updated.Certificate = message.Credential.CertificatePEM
				updated.PrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))
				updated.PendingCSR = ""
				updated.PendingKey = ""
				if err := WriteIdentity(c.identityPath, updated); err != nil {
					return fmt.Errorf("persist rotated agent credential: %w", err)
				}
				c.identity = updated
				return errCredentialRotated
			case message.DisconnectReason != "":
				return errors.New(message.DisconnectReason)
			}
		}
	}
}

func (c *channelClient) prepareCredentialRequest() (*edgeprotocol.CredentialRequest, ed25519.PrivateKey, error) {
	if c.identity.PendingCSR != "" && c.identity.PendingKey != "" {
		block, _ := pem.Decode([]byte(c.identity.PendingKey))
		if block == nil || block.Type != "PRIVATE KEY" {
			return nil, nil, errors.New("decode pending agent private key")
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("parse pending agent private key: %w", err)
		}
		privateKey, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, nil, errors.New("pending agent private key is not Ed25519")
		}
		return &edgeprotocol.CredentialRequest{CSRPEM: c.identity.PendingCSR}, privateKey, nil
	}

	request, privateKey, err := newCredentialRequest(c.identity.NodeID)
	if err != nil {
		return nil, nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, nil, err
	}
	updated := c.identity
	updated.PendingCSR = request.CSRPEM
	updated.PendingKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))
	if err := WriteIdentity(c.identityPath, updated); err != nil {
		return nil, nil, fmt.Errorf("persist pending agent credential: %w", err)
	}
	c.identity = updated
	return request, privateKey, nil
}

func (c *channelClient) runTasks(ctx context.Context, tasks <-chan edgeprotocol.AgentTask, outbound chan<- *edgeprotocol.ClientMessage) {
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-tasks:
			result := c.executeTask(ctx, task)
			if sendClientMessage(ctx, outbound, &edgeprotocol.ClientMessage{TaskResult: &result}) != nil {
				return
			}
		}
	}
}

func (c *channelClient) executeTask(ctx context.Context, task edgeprotocol.AgentTask) edgeprotocol.AgentTaskResult {
	result := edgeprotocol.AgentTaskResult{TaskID: task.ID}
	var value any
	var err error
	switch task.Kind {
	case edgeprotocol.TaskApplySiteConfig:
		var config SiteConfig
		if err = json.Unmarshal(task.Payload, &config); err == nil {
			err = c.configs.ApplySite(config)
		}
		value = map[string]any{
			"site_id": config.SiteID, "version": config.Version,
			"config_version": c.configs.ConfigVersion(), "applied": err == nil,
		}
	case edgeprotocol.TaskPurgeSite:
		var request edgeprotocol.PurgeRequest
		if err = json.Unmarshal(task.Payload, &request); err == nil {
			err = c.configs.Purge(ctx, request)
		}
	case edgeprotocol.TaskNodeCacheConfig:
		var config NodeConfig
		if err = json.Unmarshal(task.Payload, &config); err == nil {
			err = c.nodeConfigs.Set(config)
		}
		if err == nil {
			err = c.configs.SetNodeConfig(c.nodeConfigs.Get())
		}
		value = c.nodeConfigs.Get()
	default:
		err = fmt.Errorf("unsupported agent task kind %q", task.Kind)
	}
	result.Success = err == nil
	if err != nil {
		result.Error = err.Error()
	} else if value != nil {
		result.Result, _ = json.Marshal(value)
	}
	return result
}

func (c *channelClient) tlsConfig() (*tls.Config, error) {
	pair, err := tls.X509KeyPair([]byte(c.identity.Certificate), []byte(c.identity.PrivateKey))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(c.identity.CACertificate)) {
		return nil, errors.New("load control-plane CA certificate")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, ServerName: c.identity.ServerName,
		RootCAs: roots, Certificates: []tls.Certificate{pair}, NextProtos: []string{"h2"},
	}, nil
}

func newCredentialRequest(nodeID string) (*edgeprotocol.CredentialRequest, ed25519.PrivateKey, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	requestDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: nodeID},
	}, privateKey)
	if err != nil {
		return nil, nil, err
	}
	return &edgeprotocol.CredentialRequest{CSRPEM: string(pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE REQUEST", Bytes: requestDER,
	}))}, privateKey, nil
}

func sendClientMessage(ctx context.Context, target chan<- *edgeprotocol.ClientMessage, message *edgeprotocol.ClientMessage) error {
	select {
	case target <- message:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func reconnectDelay(attempt int) time.Duration {
	seconds := math.Pow(2, float64(min(attempt, 5)))
	return time.Duration(seconds) * time.Second
}
