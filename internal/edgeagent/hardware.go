package edgeagent

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"

	"goveto-edge/internal/edgeprotocol"
)

const defaultDiskBenchmarkBytes int64 = 16 << 20

type HardwareProfile = edgeprotocol.NodeHardwareProfile

func CollectHardwareProfile(ctx context.Context, directory string, benchmarkBytes int64) HardwareProfile {
	profile := HardwareProfile{
		Architecture: runtime.GOARCH,
		CPUModel:     cpuModel(ctx),
		MeasuredAt:   time.Now().UTC(),
	}
	if benchmarkBytes <= 0 {
		benchmarkBytes = defaultDiskBenchmarkBytes
	}
	written, elapsed, err := benchmarkDiskWrite(ctx, directory, benchmarkBytes)
	if err != nil {
		profile.DiskBenchmarkError = err.Error()
		return profile
	}
	seconds := elapsed.Seconds()
	if seconds <= 0 {
		profile.DiskBenchmarkError = "benchmark duration was zero"
		return profile
	}
	rate := uint64(float64(written) / seconds)
	bytes := uint64(written)
	durationMS := max(elapsed.Milliseconds(), 1)
	profile.CacheDiskWriteBytesPerSecond = &rate
	profile.BenchmarkBytes = &bytes
	profile.BenchmarkDurationMS = &durationMS
	return profile
}

func RunHardwareBenchmarkCommand(args []string, output io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "benchmark" {
		return false, nil
	}
	flags := flag.NewFlagSet("benchmark", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	directory := flags.String("directory", "/opt/goveto-edge/cache", "cache directory")
	benchmarkBytes := flags.Int64("bytes", defaultDiskBenchmarkBytes, "bytes to write")
	if err := flags.Parse(args[1:]); err != nil {
		return true, err
	}
	if strings.TrimSpace(*directory) == "" {
		return true, fmt.Errorf("benchmark directory is required")
	}
	profile := CollectHardwareProfile(context.Background(), *directory, *benchmarkBytes)
	return true, json.NewEncoder(output).Encode(profile)
}

func cpuModel(ctx context.Context) string {
	info, err := cpu.InfoWithContext(ctx)
	if err == nil {
		for _, item := range info {
			if model := strings.TrimSpace(item.ModelName); model != "" {
				return model
			}
		}
	}
	return runtime.GOARCH
}

func benchmarkDiskWrite(ctx context.Context, directory string, totalBytes int64) (int64, time.Duration, error) {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return 0, 0, fmt.Errorf("create cache directory: %w", err)
	}
	file, err := os.CreateTemp(directory, ".goveto-write-benchmark-*")
	if err != nil {
		return 0, 0, fmt.Errorf("create benchmark file: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)

	buffer := make([]byte, 1<<20)
	var written int64
	started := time.Now()
	for written < totalBytes {
		select {
		case <-ctx.Done():
			file.Close()
			return written, time.Since(started), ctx.Err()
		default:
		}
		remaining := totalBytes - written
		chunk := buffer
		if remaining < int64(len(buffer)) {
			chunk = buffer[:remaining]
		}
		n, writeErr := file.Write(chunk)
		written += int64(n)
		if writeErr != nil {
			file.Close()
			return written, time.Since(started), fmt.Errorf("write benchmark file: %w", writeErr)
		}
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return written, time.Since(started), fmt.Errorf("sync benchmark file: %w", err)
	}
	elapsed := time.Since(started)
	if err := file.Close(); err != nil {
		return written, elapsed, fmt.Errorf("close benchmark file: %w", err)
	}
	return written, elapsed, nil
}
