package edgeprotocol

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/status"
)

const (
	TaskApplySiteConfig = "APPLY_SITE_CONFIG"
	TaskPurgeSite       = "PURGE_SITE"
	TaskNodeCacheConfig = "NODE_CACHE_CONFIG"
	TaskSyncGeoIP       = "SYNC_GEOIP_DATABASE"
)

type GeoIPStatus struct {
	SHA256     string `json:"sha256,omitempty"`
	Size       int64  `json:"size,omitempty"`
	BuildEpoch uint64 `json:"build_epoch,omitempty"`
}

type GeoIPSyncPayload = GeoIPStatus

type GeoIPDownloadRequest struct {
	NodeID string `json:"node_id"`
	SHA256 string `json:"sha256"`
}

type GeoIPChunk struct {
	Offset int64  `json:"offset"`
	Data   []byte `json:"data"`
}

type AgentHello struct {
	NodeID       string            `json:"node_id"`
	AgentVersion string            `json:"agent_version,omitempty"`
	CacheConfig  NodeCacheConfig   `json:"cache_config"`
	SiteVersions map[string]uint64 `json:"site_versions,omitempty"`
	GeoIP        GeoIPStatus       `json:"geoip"`
}

type AgentHeartbeat struct {
	CacheConfig  NodeCacheConfig   `json:"cache_config"`
	SiteVersions map[string]uint64 `json:"site_versions,omitempty"`
	QueueBytes   uint64            `json:"queue_bytes,omitempty"`
	QueueRecords uint64            `json:"queue_records,omitempty"`
	DroppedLogs  uint64            `json:"dropped_logs,omitempty"`
	GeoIP        GeoIPStatus       `json:"geoip"`
}

type AgentTask struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type AgentTaskResult struct {
	TaskID  string          `json:"task_id"`
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type AgentLogBatch struct {
	FirstID uint64      `json:"first_id,omitempty"`
	Through uint64      `json:"through"`
	Bytes   uint64      `json:"bytes,omitempty"`
	Records []LogRecord `json:"records"`
}

type AgentLogAck struct {
	Through      uint64 `json:"through,omitempty"`
	Accepted     bool   `json:"accepted"`
	RetryAfterMS int    `json:"retry_after_ms,omitempty"`
	Error        string `json:"error,omitempty"`
}

type CredentialRequest struct {
	CSRPEM string `json:"csr_pem"`
}

type CredentialUpdate struct {
	CertificatePEM string `json:"certificate_pem"`
	NotAfter       string `json:"not_after"`
}

type ClientMessage struct {
	Hello             *AgentHello        `json:"hello,omitempty"`
	Heartbeat         *AgentHeartbeat    `json:"heartbeat,omitempty"`
	TaskResult        *AgentTaskResult   `json:"task_result,omitempty"`
	Logs              *AgentLogBatch     `json:"logs,omitempty"`
	CredentialRequest *CredentialRequest `json:"credential_request,omitempty"`
}

type ServerWelcome struct {
	HeartbeatSeconds   int `json:"heartbeat_seconds"`
	MaxInflightTasks   int `json:"max_inflight_tasks"`
	RotateBeforeHours  int `json:"rotate_before_hours"`
	MaxLogBatchRecords int `json:"max_log_batch_records,omitempty"`
	MaxLogBatchBytes   int `json:"max_log_batch_bytes,omitempty"`
}

type ServerMessage struct {
	Welcome          *ServerWelcome    `json:"welcome,omitempty"`
	Task             *AgentTask        `json:"task,omitempty"`
	LogsAck          *AgentLogAck      `json:"logs_ack,omitempty"`
	RotateCredential bool              `json:"rotate_credential,omitempty"`
	Credential       *CredentialUpdate `json:"credential,omitempty"`
	DisconnectReason string            `json:"disconnect_reason,omitempty"`
}

// JSONCodec is used intentionally: the management protocol is private, versioned
// as a whole, and avoids requiring protoc in edge-agent build environments.
type JSONCodec struct{}

func (JSONCodec) Name() string                           { return "json" }
func (JSONCodec) Marshal(value any) ([]byte, error)      { return json.Marshal(value) }
func (JSONCodec) Unmarshal(data []byte, value any) error { return json.Unmarshal(data, value) }

func init() { encoding.RegisterCodec(JSONCodec{}) }

type ManagementClient interface {
	Connect(ctx context.Context, opts ...grpc.CallOption) (ManagementConnectClient, error)
	DownloadGeoIP(ctx context.Context, request *GeoIPDownloadRequest, opts ...grpc.CallOption) (ManagementDownloadGeoIPClient, error)
}

func (c *managementClient) DownloadGeoIP(ctx context.Context, request *GeoIPDownloadRequest, opts ...grpc.CallOption) (ManagementDownloadGeoIPClient, error) {
	stream, err := c.connection.NewStream(ctx, &ManagementServiceDesc.Streams[1], "/goveto.edge.Management/DownloadGeoIP", opts...)
	if err != nil {
		return nil, err
	}
	if err := stream.SendMsg(request); err != nil {
		return nil, err
	}
	if err := stream.CloseSend(); err != nil {
		return nil, err
	}
	return &managementDownloadGeoIPClient{ClientStream: stream}, nil
}

type ManagementDownloadGeoIPClient interface {
	Recv() (*GeoIPChunk, error)
	grpc.ClientStream
}
type managementDownloadGeoIPClient struct{ grpc.ClientStream }

func (c *managementDownloadGeoIPClient) Recv() (*GeoIPChunk, error) {
	chunk := new(GeoIPChunk)
	if err := c.RecvMsg(chunk); err != nil {
		return nil, err
	}
	return chunk, nil
}

type managementClient struct{ connection grpc.ClientConnInterface }

func NewManagementClient(connection grpc.ClientConnInterface) ManagementClient {
	return &managementClient{connection: connection}
}

func (c *managementClient) Connect(ctx context.Context, opts ...grpc.CallOption) (ManagementConnectClient, error) {
	stream, err := c.connection.NewStream(ctx, &ManagementServiceDesc.Streams[0], "/goveto.edge.Management/Connect", opts...)
	if err != nil {
		return nil, err
	}
	return &managementConnectClient{ClientStream: stream}, nil
}

type ManagementConnectClient interface {
	Send(*ClientMessage) error
	Recv() (*ServerMessage, error)
	grpc.ClientStream
}

type managementConnectClient struct{ grpc.ClientStream }

func (c *managementConnectClient) Send(message *ClientMessage) error { return c.SendMsg(message) }
func (c *managementConnectClient) Recv() (*ServerMessage, error) {
	message := new(ServerMessage)
	if err := c.RecvMsg(message); err != nil {
		return nil, err
	}
	return message, nil
}

type ManagementServer interface {
	Connect(ManagementConnectServer) error
}

type ManagementGeoIPServer interface {
	DownloadGeoIP(*GeoIPDownloadRequest, ManagementDownloadGeoIPServer) error
}

type ManagementDownloadGeoIPServer interface {
	Send(*GeoIPChunk) error
	grpc.ServerStream
}
type managementDownloadGeoIPServer struct{ grpc.ServerStream }

func (s *managementDownloadGeoIPServer) Send(chunk *GeoIPChunk) error { return s.SendMsg(chunk) }

type ManagementConnectServer interface {
	Send(*ServerMessage) error
	Recv() (*ClientMessage, error)
	grpc.ServerStream
}

type managementConnectServer struct{ grpc.ServerStream }

func (s *managementConnectServer) Send(message *ServerMessage) error { return s.SendMsg(message) }
func (s *managementConnectServer) Recv() (*ClientMessage, error) {
	message := new(ClientMessage)
	if err := s.RecvMsg(message); err != nil {
		return nil, err
	}
	return message, nil
}

func RegisterManagementServer(server grpc.ServiceRegistrar, implementation ManagementServer) {
	server.RegisterService(&ManagementServiceDesc, implementation)
}

func managementConnectHandler(server any, stream grpc.ServerStream) error {
	return server.(ManagementServer).Connect(&managementConnectServer{ServerStream: stream})
}

func managementDownloadGeoIPHandler(server any, stream grpc.ServerStream) error {
	request := new(GeoIPDownloadRequest)
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	downloader, ok := server.(ManagementGeoIPServer)
	if !ok {
		return status.Error(codes.Unimplemented, "GeoIP download is not supported")
	}
	return downloader.DownloadGeoIP(request, &managementDownloadGeoIPServer{ServerStream: stream})
}

var ManagementServiceDesc = grpc.ServiceDesc{
	ServiceName: "goveto.edge.Management",
	HandlerType: (*ManagementServer)(nil),
	Streams: []grpc.StreamDesc{{
		StreamName:    "Connect",
		Handler:       managementConnectHandler,
		ServerStreams: true,
		ClientStreams: true,
	}, {
		StreamName: "DownloadGeoIP", Handler: managementDownloadGeoIPHandler, ServerStreams: true,
	}},
}
