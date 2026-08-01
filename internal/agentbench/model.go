package agentbench

import "time"

const SchemaVersion = "1.2"

type Protocol string

const (
	ProtocolH1 Protocol = "h1"
	ProtocolH2 Protocol = "h2"
	ProtocolH3 Protocol = "h3"
)

type Suite string

type Variant string

const (
	VariantFull    Variant = "full"
	VariantControl Variant = "control"
)

const (
	SuitePR       Suite = "pr"
	SuiteNightly  Suite = "nightly"
	SuiteCapacity Suite = "capacity"
	SuiteSoak     Suite = "soak"
)

type Config struct {
	RunnerID                  string
	Suite                     Suite
	Protocol                  Protocol
	Scenario                  string
	Method                    string
	URL                       string
	Host                      string
	Concurrency               int
	Duration                  time.Duration
	Warmup                    time.Duration
	Repeats                   int
	RequestTimeout            time.Duration
	ExpectedStatus            int
	AllowedStatuses           []int
	MinStatusCounts           map[int]uint64
	MaxStatusCounts           map[int]uint64
	ExpectedSHA256            string
	ExpectedHeaders           map[string]string
	AllowedHeaders            map[string][]string
	MaxHeaderRatios           map[string]map[string]float64
	RequestHeaders            map[string]string
	CaptureHeaders            []string
	InsecureSkipVerify        bool
	NewConnection             bool
	UniqueQuery               bool
	UniqueQueryNamespace      string
	UniqueQueryCardinality    int
	Cooldown                  time.Duration
	CapacityProbe             bool
	AgentPID                  int32
	AgentMetricsURL           string
	AgentGCURL                string
	SampleInterval            time.Duration
	MinCacheHits              uint64
	MinCacheMisses            uint64
	MinCacheEvictions         uint64
	MaxCapturedValues         int
	MaxLoadCPUPercent         float64
	MaxAgentRSSBytes          uint64
	MaxAgentRSSGrowthBytes    uint64
	Variant                   Variant
	RequireCompleteAccessLogs bool
}

type Report struct {
	SchemaVersion string            `json:"schema_version"`
	RunnerID      string            `json:"runner_id"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Commit        string            `json:"commit,omitempty"`
	BinarySHA256  string            `json:"agent_binary_sha256,omitempty"`
	Platform      Platform          `json:"platform"`
	Scenario      Scenario          `json:"scenario"`
	Runs          []Run             `json:"runs"`
	Summary       Metrics           `json:"summary"`
	Validity      Validity          `json:"validity"`
	ErrorCounts   map[string]uint64 `json:"error_counts,omitempty"`
	Baseline      *BaselineDecision `json:"baseline,omitempty"`
	Control       *ControlDecision  `json:"control,omitempty"`
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
	Suite                     Suite                         `json:"suite"`
	Name                      string                        `json:"name"`
	Method                    string                        `json:"method"`
	Protocol                  Protocol                      `json:"protocol"`
	URL                       string                        `json:"url"`
	Concurrency               int                           `json:"concurrency"`
	DurationMS                int64                         `json:"duration_ms"`
	WarmupMS                  int64                         `json:"warmup_ms"`
	Repeats                   int                           `json:"repeats"`
	NewConnection             bool                          `json:"new_connection"`
	ExpectedStatus            int                           `json:"expected_status"`
	AllowedStatuses           []int                         `json:"allowed_statuses,omitempty"`
	MinStatusCounts           map[int]uint64                `json:"min_status_counts,omitempty"`
	MaxStatusCounts           map[int]uint64                `json:"max_status_counts,omitempty"`
	ExpectedSHA256            string                        `json:"expected_sha256,omitempty"`
	ExpectedHeaders           map[string]string             `json:"expected_headers,omitempty"`
	AllowedHeaders            map[string][]string           `json:"allowed_headers,omitempty"`
	MaxHeaderRatios           map[string]map[string]float64 `json:"max_header_ratios,omitempty"`
	RequestHeaders            map[string]string             `json:"request_headers,omitempty"`
	CaptureHeaders            []string                      `json:"capture_headers,omitempty"`
	UniqueQuery               bool                          `json:"unique_query,omitempty"`
	UniqueQueryCardinality    int                           `json:"unique_query_cardinality,omitempty"`
	CooldownMS                int64                         `json:"cooldown_ms,omitempty"`
	CapacityProbe             bool                          `json:"capacity_probe,omitempty"`
	MinCacheHits              uint64                        `json:"min_cache_hits,omitempty"`
	MinCacheMisses            uint64                        `json:"min_cache_misses,omitempty"`
	MinCacheEvictions         uint64                        `json:"min_cache_evictions,omitempty"`
	MaxCapturedValues         int                           `json:"max_captured_values,omitempty"`
	MaxLoadCPUPercent         float64                       `json:"max_load_cpu_percent,omitempty"`
	MaxAgentRSSBytes          uint64                        `json:"max_agent_rss_bytes,omitempty"`
	MaxAgentRSSGrowthBytes    uint64                        `json:"max_agent_rss_growth_bytes,omitempty"`
	PostCooldownGC            bool                          `json:"post_cooldown_gc,omitempty"`
	Variant                   Variant                       `json:"variant"`
	RequireCompleteAccessLogs bool                          `json:"require_complete_access_logs,omitempty"`
}

type Run struct {
	Index             int               `json:"index"`
	StartedAt         time.Time         `json:"started_at"`
	Metrics           Metrics           `json:"metrics"`
	Resources         ResourceSummary   `json:"resources,omitempty"`
	Samples           []TimeSeriesPoint `json:"samples,omitempty"`
	Errors            []string          `json:"errors,omitempty"`
	ErrorCounts       map[string]uint64 `json:"error_counts,omitempty"`
	CleanupErrors     []string          `json:"cleanup_errors,omitempty"`
	EnvironmentErrors []string          `json:"environment_errors,omitempty"`
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
	HTTPStatusCounts   map[int]uint64               `json:"http_status_counts,omitempty"`
}

type ResourceSummary struct {
	RSSBytesStart           uint64     `json:"rss_bytes_start,omitempty"`
	FDsStart                int32      `json:"fds_start,omitempty"`
	ConnectionsStart        int        `json:"connections_start,omitempty"`
	HeapBytesStart          uint64     `json:"heap_bytes_start,omitempty"`
	GoroutinesStart         int        `json:"goroutines_start,omitempty"`
	CPUPercentMax           float64    `json:"cpu_percent_max,omitempty"`
	RSSBytesMax             uint64     `json:"rss_bytes_max,omitempty"`
	RSSBytesGrowth          uint64     `json:"rss_bytes_growth,omitempty"`
	FDsMax                  int32      `json:"fds_max,omitempty"`
	ConnectionsMax          int        `json:"connections_max,omitempty"`
	ReadBytes               uint64     `json:"read_bytes,omitempty"`
	WriteBytes              uint64     `json:"write_bytes,omitempty"`
	HeapBytesMax            uint64     `json:"heap_bytes_max,omitempty"`
	HeapInuseBytesStart     uint64     `json:"heap_inuse_bytes_start,omitempty"`
	HeapInuseBytesMax       uint64     `json:"heap_inuse_bytes_max,omitempty"`
	HeapIdleBytesStart      uint64     `json:"heap_idle_bytes_start,omitempty"`
	HeapIdleBytesMax        uint64     `json:"heap_idle_bytes_max,omitempty"`
	HeapReleasedBytesStart  uint64     `json:"heap_released_bytes_start,omitempty"`
	HeapReleasedBytesMax    uint64     `json:"heap_released_bytes_max,omitempty"`
	AllocationRateMax       float64    `json:"allocation_bytes_per_second_max,omitempty"`
	GoroutinesMax           int        `json:"goroutines_max,omitempty"`
	QueueBytesMax           uint64     `json:"log_queue_bytes_max,omitempty"`
	QueueRecordsMax         uint64     `json:"log_queue_records_max,omitempty"`
	DroppedLogsMax          uint64     `json:"dropped_logs_max,omitempty"`
	BufferBytesMax          uint64     `json:"log_buffer_bytes_max,omitempty"`
	BufferRecordsMax        uint64     `json:"log_buffer_records_max,omitempty"`
	MemoryDroppedLogsDelta  uint64     `json:"memory_dropped_logs_delta,omitempty"`
	DiskDroppedLogsDelta    uint64     `json:"disk_dropped_logs_delta,omitempty"`
	CommittedBatchesDelta   uint64     `json:"committed_log_batches_delta,omitempty"`
	CommittedRecordsDelta   uint64     `json:"committed_log_records_delta,omitempty"`
	AverageBatchSize        float64    `json:"average_log_batch_size,omitempty"`
	LastPersistError        string     `json:"last_log_persist_error,omitempty"`
	LastPersistSuccess      *time.Time `json:"last_log_persist_success,omitempty"`
	CacheHitsDelta          uint64     `json:"cache_hits_delta,omitempty"`
	CacheMissesDelta        uint64     `json:"cache_misses_delta,omitempty"`
	CacheEvictionsDelta     uint64     `json:"cache_evictions_delta,omitempty"`
	RSSBytesEnd             uint64     `json:"rss_bytes_end,omitempty"`
	FDsEnd                  int32      `json:"fds_end,omitempty"`
	ConnectionsEnd          int        `json:"connections_end,omitempty"`
	HeapBytesEnd            uint64     `json:"heap_bytes_end,omitempty"`
	HeapInuseBytesEnd       uint64     `json:"heap_inuse_bytes_end,omitempty"`
	HeapIdleBytesEnd        uint64     `json:"heap_idle_bytes_end,omitempty"`
	HeapReleasedBytesEnd    uint64     `json:"heap_released_bytes_end,omitempty"`
	RSSBytesPostGC          uint64     `json:"rss_bytes_post_gc,omitempty"`
	HeapBytesPostGC         uint64     `json:"heap_bytes_post_gc,omitempty"`
	HeapInuseBytesPostGC    uint64     `json:"heap_inuse_bytes_post_gc,omitempty"`
	HeapIdleBytesPostGC     uint64     `json:"heap_idle_bytes_post_gc,omitempty"`
	HeapReleasedBytesPostGC uint64     `json:"heap_released_bytes_post_gc,omitempty"`
	GoroutinesEnd           int        `json:"goroutines_end,omitempty"`
	QueueBytesEnd           uint64     `json:"log_queue_bytes_end,omitempty"`
	QueueRecordsEnd         uint64     `json:"log_queue_records_end,omitempty"`
	BufferBytesEnd          uint64     `json:"log_buffer_bytes_end,omitempty"`
	BufferRecordsEnd        uint64     `json:"log_buffer_records_end,omitempty"`
	memoryDroppedLogsStart  uint64
	diskDroppedLogsStart    uint64
	committedBatchesStart   uint64
	committedRecordsStart   uint64
}

type TimeSeriesPoint struct {
	At                 time.Time  `json:"at"`
	Phase              string     `json:"phase,omitempty"`
	Requests           uint64     `json:"requests"`
	Failures           uint64     `json:"failures"`
	RPS                float64    `json:"rps"`
	CPUPercent         float64    `json:"cpu_percent,omitempty"`
	RSSBytes           uint64     `json:"rss_bytes,omitempty"`
	FDs                int32      `json:"fds,omitempty"`
	Connections        int        `json:"connections,omitempty"`
	HeapBytes          uint64     `json:"heap_bytes,omitempty"`
	HeapInuseBytes     uint64     `json:"heap_inuse_bytes,omitempty"`
	HeapIdleBytes      uint64     `json:"heap_idle_bytes,omitempty"`
	HeapReleasedBytes  uint64     `json:"heap_released_bytes,omitempty"`
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

type ResultStatus string

const (
	ResultPass            ResultStatus = "PASS"
	ResultProductFail     ResultStatus = "PRODUCT_FAIL"
	ResultLoadSaturated   ResultStatus = "LOAD_SATURATED"
	ResultTargetSaturated ResultStatus = "TARGET_SATURATED"
	ResultEnvInvalid      ResultStatus = "ENV_INVALID"
)

type Validity struct {
	Valid             bool         `json:"valid"`
	Status            ResultStatus `json:"status"`
	Reasons           []string     `json:"reasons,omitempty"`
	RPSCoefficientVar float64      `json:"rps_coefficient_of_variation"`
	LoadCPUPercentMax float64      `json:"load_cpu_percent_max,omitempty"`
}

type BaselineDecision struct {
	Passed      bool               `json:"passed"`
	Comparisons []MetricComparison `json:"comparisons"`
	Reason      string             `json:"reason,omitempty"`
}

type ControlDecision struct {
	Passed       bool    `json:"passed"`
	FullRPS      float64 `json:"full_rps"`
	ControlRPS   float64 `json:"control_rps"`
	Ratio        float64 `json:"ratio"`
	MinimumRatio float64 `json:"minimum_ratio"`
	Reason       string  `json:"reason,omitempty"`
}

type MetricComparison struct {
	Metric        string  `json:"metric"`
	Baseline      float64 `json:"baseline"`
	Current       float64 `json:"current"`
	ChangePercent float64 `json:"change_percent"`
	LimitPercent  float64 `json:"limit_percent"`
	Passed        bool    `json:"passed"`
}
