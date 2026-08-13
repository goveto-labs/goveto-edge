// Package e2e drives the real edge agent as a black box over the production
// mTLS management protocol. A stub control-plane gateway delivers real
// APPLY_SITE_CONFIG / PURGE tasks to a real edgeagent.Agent, which serves
// traffic through its embedded Caddy data plane. This is the closest a test
// can get to production without spinning up PostgreSQL + Redis + the control
// API, and it exercises the same gRPC + JSON task codec the control plane uses.
package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/google/uuid"

	_ "github.com/caddyserver/caddy/v2/modules/standard"

	_ "goveto-edge/caddy/cachematch"
	_ "goveto-edge/caddy/govetocache"
	cachefs "goveto-edge/caddy/simplefs"
	_ "goveto-edge/caddy/waf"
	"goveto-edge/internal/edgeagent"
	"goveto-edge/internal/edgecontrol"
	"goveto-edge/internal/edgeprotocol"
	"goveto-edge/internal/node"
	cachepolicy "goveto-edge/internal/policy"
)

// stubGateway is a minimal control-plane mTLS gateway. It welcomes the agent,
// acks every log batch (so the agent never blocks on log backpressure), and
// delivers tasks pushed through sendTask, collecting each result so callers can
// wait for an APPLY_SITE_CONFIG or PURGE to settle before asserting on traffic.
type stubGateway struct {
	edgeprotocol.ManagementServer

	taskQueue chan edgeprotocol.AgentTask
	mu        sync.Mutex
	waiters   map[string]chan edgeprotocol.AgentTaskResult
	connects  int
	connected chan struct{}
	lastHello edgeprotocol.AgentHello
}

func newStubGateway() *stubGateway {
	return &stubGateway{
		taskQueue: make(chan edgeprotocol.AgentTask, 64),
		waiters:   map[string]chan edgeprotocol.AgentTaskResult{},
		connected: make(chan struct{}, 16),
	}
}

func (g *stubGateway) Connect(stream edgeprotocol.ManagementConnectServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.Hello == nil || first.Hello.NodeID == "" {
		return errors.New("first message must be an agent hello")
	}
	g.mu.Lock()
	g.lastHello = *first.Hello
	g.connects++
	g.mu.Unlock()
	select {
	case g.connected <- struct{}{}:
	default:
	}

	if err := stream.Send(&edgeprotocol.ServerMessage{Welcome: &edgeprotocol.ServerWelcome{
		HeartbeatSeconds: 5, MaxInflightTasks: 16, RotateBeforeHours: 168,
		MaxLogBatchRecords: 2000, MaxLogBatchBytes: 4 << 20,
	}}); err != nil {
		return err
	}

	// Reader: drain agent messages (results, heartbeats, logs) and ack logs so
	// the access-log pipeline never applies backpressure.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			message, recvErr := stream.Recv()
			if recvErr != nil {
				return
			}
			if message.TaskResult != nil {
				g.deliverResult(message.TaskResult.TaskID, *message.TaskResult)
			}
			if message.Logs != nil {
				_ = stream.Send(&edgeprotocol.ServerMessage{LogsAck: &edgeprotocol.AgentLogAck{
					Through: message.Logs.Through, Accepted: true,
				}})
			}
		}
	}()

	// Sender: push queued tasks until the stream dies.
	for {
		select {
		case task := <-g.taskQueue:
			if err := stream.Send(&edgeprotocol.ServerMessage{Task: &task}); err != nil {
				// Re-queue so a reconnect redelivers it.
				select {
				case g.taskQueue <- task:
				default:
				}
				<-readerDone
				return err
			}
		case <-readerDone:
			return nil
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// deliverResult hands a task result to the waiting sender (if any).
func (g *stubGateway) deliverResult(taskID string, result edgeprotocol.AgentTaskResult) {
	g.mu.Lock()
	ch := g.waiters[taskID]
	delete(g.waiters, taskID)
	g.mu.Unlock()
	if ch != nil {
		select {
		case ch <- result:
		default:
		}
	}
}

// sendTask delivers a task and blocks until the agent reports a result, so a
// test never probes traffic before the configuration has actually been applied.
func (g *stubGateway) sendTask(ctx context.Context, task edgeprotocol.AgentTask) (edgeprotocol.AgentTaskResult, error) {
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	resultCh := make(chan edgeprotocol.AgentTaskResult, 1)
	g.mu.Lock()
	g.waiters[task.ID] = resultCh
	g.mu.Unlock()

	select {
	case g.taskQueue <- task:
	case <-ctx.Done():
		g.clearWaiter(task.ID)
		return edgeprotocol.AgentTaskResult{}, ctx.Err()
	}

	select {
	case result := <-resultCh:
		return result, nil
	case <-ctx.Done():
		g.clearWaiter(task.ID)
		return edgeprotocol.AgentTaskResult{}, ctx.Err()
	}
}

func (g *stubGateway) clearWaiter(taskID string) {
	g.mu.Lock()
	delete(g.waiters, taskID)
	g.mu.Unlock()
}

func (g *stubGateway) waitForConnect(ctx context.Context) error {
	select {
	case <-g.connected:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// harness wires together a stub gateway, a real mTLS authority + node identity,
// and a real edgeagent.Agent serving on a dedicated HTTP port.
type harness struct {
	t           *testing.T
	gateway     *stubGateway
	grpcServer  *grpc.Server
	listener    net.Listener
	gatewayAddr string
	authority   *edgecontrol.Authority
	nodeID      string
	identity    edgeagent.Identity
	agent       *edgeagent.Agent
	dataDir     string
	cacheDir    string
	httpPort    int
	agentDone   chan error
	cancel      context.CancelFunc
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, gateway: newStubGateway(), nodeID: uuid.NewString(), cancel: cancel}

	// A real mTLS authority. The server certificate is pinned to 127.0.0.1 so
	// the agent verifies it against the gateway's loopback address.
	gatewayListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	h.gatewayAddr = gatewayListener.Addr().String()
	h.listener = gatewayListener
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}
	cipher, err := node.NewCredentialCipher(base64.StdEncoding.EncodeToString(masterKey))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	h.authority, err = edgecontrol.NewAuthority(cipher, h.gatewayAddr)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	h.grpcServer = grpc.NewServer(
		grpc.Creds(credentials.NewTLS(h.authority.ServerTLSConfig())),
		grpc.ForceServerCodec(edgeprotocol.JSONCodec{}),
	)
	gatewayServer := h.grpcServer
	edgeprotocol.RegisterManagementServer(gatewayServer, h.gateway)
	go func() { _ = gatewayServer.Serve(gatewayListener) }()
	t.Cleanup(gatewayServer.Stop)

	bundle, err := h.authority.IssueNode(h.nodeID)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	h.identity = edgeagent.Identity{
		NodeID: bundle.NodeID, GatewayAddress: bundle.GatewayAddress, ServerName: bundle.ServerName,
		CACertificate: bundle.CACertificate, Certificate: bundle.Certificate, PrivateKey: bundle.PrivateKey,
	}

	h.dataDir = t.TempDir()
	h.cacheDir = filepath.Join(h.dataDir, "cache")
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	h.httpPort = httpListener.Addr().(*net.TCPAddr).Port
	_ = httpListener.Close() // the agent rebinds it via EDGE_USER_LISTEN

	identityPath := filepath.Join(h.dataDir, "identity.json")
	if data, err := json.MarshalIndent(h.identity, "", "  "); err != nil {
		cancel()
		t.Fatal(err)
	} else if err := os.WriteFile(identityPath, data, 0o600); err != nil {
		cancel()
		t.Fatal(err)
	}

	// edgeagent.New reads these env vars; they must be set before construction.
	t.Setenv("EDGE_AGENT_IDENTITY_FILE", identityPath)
	t.Setenv("EDGE_AGENT_DATA_DIR", h.dataDir)
	t.Setenv("EDGE_USER_LISTEN", "127.0.0.1:"+strconv.Itoa(h.httpPort))
	// Keep the access log tiny and quiet so the test output stays readable.
	t.Setenv("EDGE_AGENT_LOG_MAX_BYTES", "16777216")

	// The real disk-usage probe reads actual filesystem free space; pin a huge
	// capacity so cache writes never reject in a constrained CI sandbox.
	t.Cleanup(cachefs.OverrideDiskUsageForTesting(h.cacheDir, 1<<40, 0))

	h.agent = edgeagent.New()
	h.agentDone = make(chan error, 1)
	go func() {
		h.agentDone <- h.agent.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.agentDone:
		case <-time.After(10 * time.Second):
			t.Errorf("agent did not stop within 10s")
		}
	})

	// Wait for the agent to establish its management channel before driving it.
	connectCtx, connectCancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer connectCancel()
	if err := h.gateway.waitForConnect(connectCtx); err != nil {
		t.Fatalf("agent never connected to the gateway: %v", err)
	}
	// Prime the node cache directory so subsequent site configs can write.
	h.applyNodeCacheConfig(ctx)
	return h
}

func (h *harness) applyNodeCacheConfig(ctx context.Context) {
	h.t.Helper()
	payload, _ := json.Marshal(edgeprotocol.NodeCacheConfig{
		CacheDirectory: h.cacheDir, MaxSizeBytes: 128 << 20, MaxDiskUsagePercent: 90,
	})
	if _, err := h.gateway.sendTask(ctx, edgeprotocol.AgentTask{
		ID: uuid.NewString(), Kind: edgeprotocol.TaskNodeCacheConfig, Payload: payload,
	}); err != nil {
		h.t.Fatalf("apply node cache config: %v", err)
	}
}

// applySite delivers an APPLY_SITE_CONFIG task and waits for the agent to
// acknowledge it. The returned edgeprotocol.SiteConfig is exactly what the agent
// received, so callers can inspect the version that took effect.
func (h *harness) applySite(ctx context.Context, config edgeprotocol.SiteConfig) {
	h.t.Helper()
	payload, err := json.Marshal(config)
	if err != nil {
		h.t.Fatal(err)
	}
	result, err := h.gateway.sendTask(ctx, edgeprotocol.AgentTask{
		ID: uuid.NewString(), Kind: edgeprotocol.TaskApplySiteConfig, Payload: payload,
	})
	if err != nil {
		h.t.Fatalf("apply site config %s: %v", config.SiteID, err)
	}
	if !result.Success {
		h.t.Fatalf("agent rejected site config %s: %s", config.SiteID, result.Error)
	}
}

func (h *harness) purge(ctx context.Context, request edgeprotocol.PurgeRequest) edgeprotocol.PurgeResult {
	h.t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		h.t.Fatal(err)
	}
	result, err := h.gateway.sendTask(ctx, edgeprotocol.AgentTask{
		ID: uuid.NewString(), Kind: edgeprotocol.TaskPurgeSite, Payload: payload,
	})
	if err != nil {
		h.t.Fatalf("purge %s: %v", request.SiteID, err)
	}
	if !result.Success {
		h.t.Fatalf("agent rejected purge: %s", result.Error)
	}
	var summary edgeprotocol.PurgeResult
	_ = json.Unmarshal(result.Result, &summary)
	return summary
}

// edgeResponse captures what an end user observes through the data plane.
type edgeResponse struct {
	status int
	body   string
	header http.Header
}

func (h *harness) request(ctx context.Context, host, method, path string, headers http.Header) edgeResponse {
	h.t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(h.httpPort)+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	request.Host = host
	request.Header = headers.Clone()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		h.t.Fatalf("edge request %s %s failed: %v", method, path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatalf("read edge response body: %v", err)
	}
	return edgeResponse{status: response.StatusCode, body: string(body), header: response.Header.Clone()}
}

// waitForAgentReconnect blocks until the agent has (re)established its
// management channel, used after deliberately disrupting the connection.
func (h *harness) waitForAgentReconnect(ctx context.Context) {
	h.t.Helper()
	before := func() int { h.gateway.mu.Lock(); defer h.gateway.mu.Unlock(); return h.gateway.connects }
	prior := before()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if before() > prior {
			return
		}
		select {
		case <-ctx.Done():
			h.t.Fatalf("agent did not reconnect: %v", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	h.t.Fatal("agent did not reconnect within 30s")
}

// --- shared config builders -------------------------------------------------

// catchAllCachePolicy returns a policy that caches every cacheable response for
// `ttl` seconds, matching what a default site publish would render.
func catchAllCachePolicy(ttl int) cachepolicy.CachePolicy {
	policy := cachepolicy.DefaultCachePolicy()
	policy.ResponseHeaders.XCache = true
	policy.ResponseHeaders.Age = true
	policy.Rules = []cachepolicy.CacheRule{{
		Name: "Default",
		TTL: cachepolicy.CacheTTL{
			DefaultSeconds: ttl, Status: map[string]int{"200": ttl, "301": 3600, "404": ttl},
			ClientSeconds: ttl,
		},
		Conditions: cachepolicy.CacheConditions{GroupOperator: "OR", Groups: []cachepolicy.CacheConditionGroup{{
			Operator: "OR", Rules: []cachepolicy.CacheConditionRule{{Type: "ALL"}},
		}}},
	}}
	return policy
}

func policyToMap(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// suppressCaddyLogs keeps the test output readable; Caddy logs at warn level
// during rapid reconfigurations and that is expected, not a test failure.
func init() {
	if level := slog.LevelInfo; true {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level})))
	}
}
