package agentbench

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
)

var buildCommit string

func CollectPlatform() Platform {
	platform := Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH, GoVersion: runtime.Version(), CPUCount: runtime.NumCPU()}
	if info, err := cpu.Info(); err == nil && len(info) > 0 {
		platform.CPUModel = info[0].ModelName
	}
	if info, err := host.Info(); err == nil {
		platform.Kernel = strings.TrimSpace(info.KernelVersion)
	}
	platform.ContainerCPUs = readFirst("/sys/fs/cgroup/cpuset.cpus.effective", "/sys/fs/cgroup/cpuset/cpuset.cpus")
	platform.ContainerMemory = readFirst("/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes")
	return platform
}

func GitCommit() string {
	if strings.TrimSpace(buildCommit) != "" && buildCommit != "unknown" {
		return strings.TrimSpace(buildCommit)
	}
	output, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func FileSHA256(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	digest := sha256.New()
	scanner := bufio.NewReader(file)
	if _, err := scanner.WriteTo(digest); err != nil {
		return ""
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func readFirst(paths ...string) string {
	for _, path := range paths {
		if data, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}
