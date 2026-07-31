package agentbench

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

func ReadReport(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, err
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, err
	}
	if report.SchemaVersion == "" {
		return Report{}, fmt.Errorf("report has no schema_version")
	}
	if report.SchemaVersion != SchemaVersion {
		return Report{}, fmt.Errorf("unsupported report schema_version %q", report.SchemaVersion)
	}
	return report, nil
}

func WriteArtifacts(directory string, report Report) error {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(directory, "report.json"), report); err != nil {
		return err
	}
	if err := writeMarkdown(filepath.Join(directory, "summary.md"), report); err != nil {
		return err
	}
	return writeCSV(filepath.Join(directory, "timeseries.csv"), report)
}

func writeJSON(path string, report Report) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func writeMarkdown(path string, report Report) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	status := report.Validity.Status
	if status == "" {
		status = ResultPass
		if !report.Validity.Valid {
			status = ResultEnvInvalid
		}
	}
	_, err = fmt.Fprintf(file, "# Edge Agent benchmark\n\n- Runner: `%s`\n- Scenario: `%s`\n- Protocol: `%s` (`%s`)\n- Platform: `%s/%s`\n- Result: **%s**\n\n| RPS | Success | p50 | p95 | p99 | Max | Bandwidth |\n|---:|---:|---:|---:|---:|---:|---:|\n| %.2f | %.4f%% | %.2f ms | %.2f ms | %.2f ms | %.2f ms | %.2f MiB/s |\n",
		report.RunnerID, report.Scenario.Name, report.Scenario.Protocol, report.Summary.NegotiatedProtocol, report.Platform.OS, report.Platform.Architecture, status,
		report.Summary.RPS, report.Summary.SuccessRate*100, report.Summary.P50MS, report.Summary.P95MS, report.Summary.P99MS, report.Summary.MaxMS, report.Summary.BytesPerSecond/(1<<20))
	if err != nil {
		return err
	}
	if len(report.Validity.Reasons) > 0 {
		_, _ = io.WriteString(file, "\n## Result reasons\n\n")
		for _, reason := range report.Validity.Reasons {
			_, _ = fmt.Fprintf(file, "- %s\n", reason)
		}
	}
	if len(report.ErrorCounts) > 0 {
		_, _ = io.WriteString(file, "\n## Error counts\n\n| Class | Count |\n|---|---:|\n")
		for _, class := range sortedKeys(report.ErrorCounts) {
			_, _ = fmt.Fprintf(file, "| %s | %d |\n", class, report.ErrorCounts[class])
		}
	}
	if len(report.Summary.HTTPStatusCounts) > 0 {
		_, _ = io.WriteString(file, "\n## HTTP status counts\n\n| Status | Responses |\n|---:|---:|\n")
		statuses := make([]int, 0, len(report.Summary.HTTPStatusCounts))
		for status := range report.Summary.HTTPStatusCounts {
			statuses = append(statuses, status)
		}
		sort.Ints(statuses)
		for _, status := range statuses {
			_, _ = fmt.Fprintf(file, "| %d | %d |\n", status, report.Summary.HTTPStatusCounts[status])
		}
	}
	if len(report.Summary.ResponseHeaders) > 0 {
		_, _ = io.WriteString(file, "\n## Captured response headers\n\n| Header | Value | Responses |\n|---|---|---:|\n")
		names := sortedKeys(report.Summary.ResponseHeaders)
		for _, name := range names {
			values := sortedKeys(report.Summary.ResponseHeaders[name])
			for _, value := range values {
				_, _ = fmt.Fprintf(file, "| %s | %s | %d |\n", name, value, report.Summary.ResponseHeaders[name][value])
			}
		}
	}
	cacheActivity := false
	for _, run := range report.Runs {
		cacheActivity = cacheActivity || run.Resources.CacheHitsDelta > 0 || run.Resources.CacheMissesDelta > 0 || run.Resources.CacheEvictionsDelta > 0
	}
	if cacheActivity {
		_, _ = io.WriteString(file, "\n## Cache activity\n\n| Run | Hits | Misses | Evictions |\n|---:|---:|---:|---:|\n")
		for _, run := range report.Runs {
			_, _ = fmt.Fprintf(file, "| %d | %d | %d | %d |\n", run.Index, run.Resources.CacheHitsDelta, run.Resources.CacheMissesDelta, run.Resources.CacheEvictionsDelta)
		}
	}
	if report.Baseline != nil {
		if report.Baseline.Reason != "" {
			_, _ = fmt.Fprintf(file, "\n## Baseline\n\n%s\n", report.Baseline.Reason)
		} else {
			_, _ = io.WriteString(file, "\n## Baseline\n\n| Metric | Baseline | Current | Change | Limit | Passed |\n|---|---:|---:|---:|---:|:---:|\n")
			for _, comparison := range report.Baseline.Comparisons {
				_, _ = fmt.Fprintf(file, "| %s | %.2f | %.2f | %+.2f%% | %.2f%% | %t |\n", comparison.Metric, comparison.Baseline, comparison.Current, comparison.ChangePercent, comparison.LimitPercent, comparison.Passed)
			}
		}
	}
	return nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeCSV(path string, report Report) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{"run", "at", "requests", "failures", "rps", "agent_cpu_percent", "agent_rss_bytes", "agent_fds", "agent_connections", "heap_bytes", "allocation_bytes_per_second", "gc_count", "goroutines", "log_queue_bytes", "log_queue_records", "dropped_logs", "cache_hits", "cache_misses", "cache_evictions", "log_buffer_bytes", "log_buffer_records", "memory_dropped_logs", "disk_dropped_logs", "committed_log_batches", "committed_log_records", "average_log_batch_size", "last_log_persist_error", "last_log_persist_success"}); err != nil {
		return err
	}
	for _, run := range report.Runs {
		for _, point := range run.Samples {
			record := []string{
				strconv.Itoa(run.Index), point.At.Format("2006-01-02T15:04:05.000Z07:00"), strconv.FormatUint(point.Requests, 10), strconv.FormatUint(point.Failures, 10),
				formatFloat(point.RPS), formatFloat(point.CPUPercent), strconv.FormatUint(point.RSSBytes, 10), strconv.FormatInt(int64(point.FDs), 10), strconv.Itoa(point.Connections),
				strconv.FormatUint(point.HeapBytes, 10), formatFloat(point.AllocationRate), strconv.FormatUint(uint64(point.GCCount), 10), strconv.Itoa(point.Goroutines),
				strconv.FormatUint(point.QueueBytes, 10), strconv.FormatUint(point.QueueRecords, 10), strconv.FormatUint(point.DroppedLogs, 10),
				strconv.FormatUint(point.CacheHits, 10), strconv.FormatUint(point.CacheMisses, 10), strconv.FormatUint(point.CacheEvictions, 10),
				strconv.FormatUint(point.BufferBytes, 10), strconv.FormatUint(point.BufferRecords, 10), strconv.FormatUint(point.MemoryDroppedLogs, 10),
				strconv.FormatUint(point.DiskDroppedLogs, 10), strconv.FormatUint(point.CommittedBatches, 10), strconv.FormatUint(point.CommittedRecords, 10),
				formatFloat(point.AverageBatchSize), point.LastPersistError, formatOptionalTime(point.LastPersistSuccess),
			}
			if err := writer.Write(record); err != nil {
				return err
			}
		}
	}
	return writer.Error()
}

func formatFloat(value float64) string { return strconv.FormatFloat(value, 'f', 4, 64) }

func formatOptionalTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}
