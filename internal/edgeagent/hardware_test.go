package edgeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestCollectHardwareProfileBenchmarksCacheDirectory(t *testing.T) {
	directory := t.TempDir()
	profile := CollectHardwareProfile(context.Background(), directory, 1<<20)
	if profile.CPUModel == "" || profile.Architecture == "" {
		t.Fatalf("missing hardware identity: %#v", profile)
	}
	if profile.DiskBenchmarkError != "" || profile.CacheDiskWriteBytesPerSecond == nil || *profile.CacheDiskWriteBytesPerSecond == 0 {
		t.Fatalf("disk benchmark failed: %#v", profile)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("benchmark file was not removed: %#v", entries)
	}
}

func TestRunHardwareBenchmarkCommand(t *testing.T) {
	var output bytes.Buffer
	handled, err := RunHardwareBenchmarkCommand([]string{"benchmark", "--directory", t.TempDir(), "--bytes", "1048576"}, &output)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	var profile HardwareProfile
	if err := json.Unmarshal(output.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.CacheDiskWriteBytesPerSecond == nil {
		t.Fatalf("missing benchmark rate: %#v", profile)
	}
}
