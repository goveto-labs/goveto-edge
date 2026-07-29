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
	validity := "valid"
	if !report.Validity.Valid {
		validity = "invalid"
	}
	_, err = fmt.Fprintf(file, "# Edge Agent benchmark\n\n- Scenario: `%s`\n- Protocol: `%s` (`%s`)\n- Platform: `%s/%s`\n- Result: **%s**\n\n| RPS | Success | p50 | p95 | p99 | Max | Bandwidth |\n|---:|---:|---:|---:|---:|---:|---:|\n| %.2f | %.4f%% | %.2f ms | %.2f ms | %.2f ms | %.2f ms | %.2f MiB/s |\n",
		report.Scenario.Name, report.Scenario.Protocol, report.Summary.NegotiatedProtocol, report.Platform.OS, report.Platform.Architecture, validity,
		report.Summary.RPS, report.Summary.SuccessRate*100, report.Summary.P50MS, report.Summary.P95MS, report.Summary.P99MS, report.Summary.MaxMS, report.Summary.BytesPerSecond/(1<<20))
	if err != nil {
		return err
	}
	if len(report.Validity.Reasons) > 0 {
		_, _ = io.WriteString(file, "\n## Invalid reasons\n\n")
		for _, reason := range report.Validity.Reasons {
			_, _ = fmt.Fprintf(file, "- %s\n", reason)
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
		_, _ = io.WriteString(file, "\n## Baseline\n\n| Metric | Baseline | Current | Change | Limit | Passed |\n|---|---:|---:|---:|---:|:---:|\n")
		for _, comparison := range report.Baseline.Comparisons {
			_, _ = fmt.Fprintf(file, "| %s | %.2f | %.2f | %+.2f%% | %.2f%% | %t |\n", comparison.Metric, comparison.Baseline, comparison.Current, comparison.ChangePercent, comparison.LimitPercent, comparison.Passed)
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
	if err := writer.Write([]string{"run", "at", "requests", "failures", "rps", "agent_cpu_percent", "agent_rss_bytes", "agent_fds", "agent_connections", "heap_bytes", "allocation_bytes_per_second", "gc_count", "goroutines", "log_queue_bytes", "log_queue_records", "dropped_logs", "cache_hits", "cache_misses", "cache_evictions"}); err != nil {
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
			}
			if err := writer.Write(record); err != nil {
				return err
			}
		}
	}
	return writer.Error()
}

func formatFloat(value float64) string { return strconv.FormatFloat(value, 'f', 4, 64) }
