package agentbench

import "time"

const SchemaVersion = "1.0"

type Protocol string

const (
	ProtocolH1 Protocol = "h1"
	ProtocolH2 Protocol = "h2"
	ProtocolH3 Protocol = "h3"
)

type Suite string

const (
	SuitePR       Suite = "pr"
	SuiteNightly  Suite = "nightly"
	SuiteCapacity Suite = "capacity"
	SuiteSoak     Suite = "soak"
)

type Config struct {
	Suite              Suite
	Protocol           Protocol
	Scenario           string
	URL                string
	Host               string
	Concurrency        int
	Duration           time.Duration
	Warmup             time.Duration
	Repeats            int
	RequestTimeout     time.Duration
	ExpectedStatus     int
	ExpectedSHA256     string
	ExpectedHeaders    map[string]string
	RequestHeaders     map[string]string
	CaptureHeaders     []string
	InsecureSkipVerify bool
	NewConnection      bool
	UniqueQuery        bool
	AgentPID           int32
	AgentMetricsURL    string
	SampleInterval     time.Duration
	MinCacheHits       uint64
	MinCacheMisses     uint64
	MinCacheEvictions  uint64
	MaxCapturedValues  int
}

type Report struct {
	SchemaVersion string            `json:"schema_version"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Commit        string            `json:"commit,omitempty"`
	BinarySHA256  string            `json:"agent_binary_sha256,omitempty"`
	Platform      Platform          `json:"platform"`
	Scenario      Scenario          `json:"scenario"`
	Runs          []Run             `json:"runs"`
	Summary       Metrics           `json:"summary"`
	Validity      Validity          `json:"validity"`
	Baseline      *BaselineDecision `json:"baseline,omitempty"`
}

type Platform struct {
	OS              string `json:"os"`
	Architecture    string `json:"architecture"`
	GoVersion       string `json:"go_version"`
	CPUCount        int    `json:"cpu_count"`
	CPUModel        string `json:"cpu_model,omitempty"`
	Kernel          string `json:"kernel,omitempty"`
	ContainerCPUs   string `json:"container_cpus,omitempty"`
	ContainerMemory string `json:"container_memory,omitempty"`
}

type Scenario struct {
	Suite             Suite             `json:"suite"`
	Name              string            `json:"name"`
	Protocol          Protocol          `json:"protocol"`
	URL               string            `json:"url"`
	Concurrency       int               `json:"concurrency"`
	DurationMS        int64             `json:"duration_ms"`
	WarmupMS          int64             `json:"warmup_ms"`
	Repeats           int               `json:"repeats"`
	NewConnection     bool              `json:"new_connection"`
	ExpectedStatus    int               `json:"expected_status"`
	ExpectedSHA256    string            `json:"expected_sha256,omitempty"`
	ExpectedHeaders   map[string]string `json:"expected_headers,omitempty"`
	RequestHeaders    map[string]string `json:"request_headers,omitempty"`
	CaptureHeaders    []string          `json:"capture_headers,omitempty"`
	UniqueQuery       bool              `json:"unique_query,omitempty"`
	MinCacheHits      uint64            `json:"min_cache_hits,omitempty"`
	MinCacheMisses    uint64            `json:"min_cache_misses,omitempty"`
	MinCacheEvictions uint64            `json:"min_cache_evictions,omitempty"`
	MaxCapturedValues int               `json:"max_captured_values,omitempty"`
}

type Run struct {
	Index     int               `json:"index"`
	StartedAt time.Time         `json:"started_at"`
	Metrics   Metrics           `json:"metrics"`
	Resources ResourceSummary   `json:"resources,omitempty"`
	Samples   []TimeSeriesPoint `json:"samples,omitempty"`
	Errors    []string          `json:"errors,omitempty"`
}

type Metrics struct {
	Requests           uint64                       `json:"requests"`
	Successes          uint64                       `json:"successes"`
	Failures           uint64                       `json:"failures"`
	Bytes              uint64                       `json:"bytes"`
	RPS                float64                      `json:"rps"`
	BytesPerSecond     float64                      `json:"bytes_per_second"`
	SuccessRate        float64                      `json:"success_rate"`
	P50MS              float64                      `json:"p50_ms"`
	P95MS              float64                      `json:"p95_ms"`
	P99MS              float64                      `json:"p99_ms"`
	MaxMS              float64                      `json:"max_ms"`
	TLSHandshakeMS     float64                      `json:"tls_handshake_p50_ms,omitempty"`
	TTFBMS             float64                      `json:"ttfb_p50_ms,omitempty"`
	NegotiatedProtocol string                       `json:"negotiated_protocol,omitempty"`
	ResponseHeaders    map[string]map[string]uint64 `json:"response_headers,omitempty"`
}

type ResourceSummary struct {
	CPUPercentMax          float64    `json:"cpu_percent_max,omitempty"`
	RSSBytesMax            uint64     `json:"rss_bytes_max,omitempty"`
	FDsMax                 int32      `json:"fds_max,omitempty"`
	ConnectionsMax         int        `json:"connections_max,omitempty"`
	ReadBytes              uint64     `json:"read_bytes,omitempty"`
	WriteBytes             uint64     `json:"write_bytes,omitempty"`
	HeapBytesMax           uint64     `json:"heap_bytes_max,omitempty"`
	AllocationRateMax      float64    `json:"allocation_bytes_per_second_max,omitempty"`
	GoroutinesMax          int        `json:"goroutines_max,omitempty"`
	QueueBytesMax          uint64     `json:"log_queue_bytes_max,omitempty"`
	QueueRecordsMax        uint64     `json:"log_queue_records_max,omitempty"`
	DroppedLogsMax         uint64     `json:"dropped_logs_max,omitempty"`
	BufferBytesMax         uint64     `json:"log_buffer_bytes_max,omitempty"`
	BufferRecordsMax       uint64     `json:"log_buffer_records_max,omitempty"`
	MemoryDroppedLogsDelta uint64     `json:"memory_dropped_logs_delta,omitempty"`
	DiskDroppedLogsDelta   uint64     `json:"disk_dropped_logs_delta,omitempty"`
	CommittedBatchesDelta  uint64     `json:"committed_log_batches_delta,omitempty"`
	CommittedRecordsDelta  uint64     `json:"committed_log_records_delta,omitempty"`
	AverageBatchSize       float64    `json:"average_log_batch_size,omitempty"`
	LastPersistError       string     `json:"last_log_persist_error,omitempty"`
	LastPersistSuccess     *time.Time `json:"last_log_persist_success,omitempty"`
	CacheHitsDelta         uint64     `json:"cache_hits_delta,omitempty"`
	CacheMissesDelta       uint64     `json:"cache_misses_delta,omitempty"`
	CacheEvictionsDelta    uint64     `json:"cache_evictions_delta,omitempty"`
}

type TimeSeriesPoint struct {
	At                 time.Time  `json:"at"`
	Requests           uint64     `json:"requests"`
	Failures           uint64     `json:"failures"`
	RPS                float64    `json:"rps"`
	CPUPercent         float64    `json:"cpu_percent,omitempty"`
	RSSBytes           uint64     `json:"rss_bytes,omitempty"`
	FDs                int32      `json:"fds,omitempty"`
	Connections        int        `json:"connections,omitempty"`
	HeapBytes          uint64     `json:"heap_bytes,omitempty"`
	AllocationRate     float64    `json:"allocation_bytes_per_second,omitempty"`
	GCCount            uint32     `json:"gc_count,omitempty"`
	Goroutines         int        `json:"goroutines,omitempty"`
	QueueBytes         uint64     `json:"log_queue_bytes,omitempty"`
	QueueRecords       uint64     `json:"log_queue_records,omitempty"`
	DroppedLogs        uint64     `json:"dropped_logs,omitempty"`
	BufferBytes        uint64     `json:"log_buffer_bytes,omitempty"`
	BufferRecords      uint64     `json:"log_buffer_records,omitempty"`
	MemoryDroppedLogs  uint64     `json:"memory_dropped_logs,omitempty"`
	DiskDroppedLogs    uint64     `json:"disk_dropped_logs,omitempty"`
	CommittedBatches   uint64     `json:"committed_log_batches,omitempty"`
	CommittedRecords   uint64     `json:"committed_log_records,omitempty"`
	AverageBatchSize   float64    `json:"average_log_batch_size,omitempty"`
	LastPersistError   string     `json:"last_log_persist_error,omitempty"`
	LastPersistSuccess *time.Time `json:"last_log_persist_success,omitempty"`
	CacheHits          uint64     `json:"cache_hits,omitempty"`
	CacheMisses        uint64     `json:"cache_misses,omitempty"`
	CacheEvictions     uint64     `json:"cache_evictions,omitempty"`
}

type Validity struct {
	Valid             bool     `json:"valid"`
	Reasons           []string `json:"reasons,omitempty"`
	RPSCoefficientVar float64  `json:"rps_coefficient_of_variation"`
	LoadCPUPercentMax float64  `json:"load_cpu_percent_max,omitempty"`
}

type BaselineDecision struct {
	Passed      bool               `json:"passed"`
	Comparisons []MetricComparison `json:"comparisons"`
	Reason      string             `json:"reason,omitempty"`
}

type MetricComparison struct {
	Metric        string  `json:"metric"`
	Baseline      float64 `json:"baseline"`
	Current       float64 `json:"current"`
	ChangePercent float64 `json:"change_percent"`
	LimitPercent  float64 `json:"limit_percent"`
	Passed        bool    `json:"passed"`
}
