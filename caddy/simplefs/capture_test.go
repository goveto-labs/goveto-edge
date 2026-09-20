package simplefs

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/shirou/gopsutil/v4/disk"
)

func TestConcurrentCaptureReservationsShareCapacity(t *testing.T) {
	p := newTestProvider(t, t.TempDir(), 0)
	p.limits.maxBytes = 1 << 20
	s := &Storage{provider: p}
	var accepted atomic.Int32
	var done sync.WaitGroup
	for range 16 {
		done.Go(func() {
			if s.ReserveCapture(256 << 10) {
				accepted.Add(1)
			}
		})
	}
	done.Wait()
	if accepted.Load() != 4 {
		t.Fatalf("accepted %d reservations", accepted.Load())
	}
	if p.captureRejections.Load() != 12 {
		t.Fatalf("rejected %d reservations", p.captureRejections.Load())
	}
	s.ReleaseCapture(1 << 20)
	if !s.ReserveCapture(1 << 20) {
		t.Fatal("released capacity unavailable")
	}
	s.ReleaseCapture(1 << 20)
}

func TestCaptureReservationsUnlimitedWhenMaxBytesZero(t *testing.T) {
	p := newTestProvider(t, t.TempDir(), 0)
	p.limits.maxBytes = 0
	s := &Storage{provider: p}
	if !s.ReserveCapture(64 << 20) {
		t.Fatal("unlimited provider rejected a reservation")
	}
	if p.captureRejections.Load() != 0 {
		t.Fatalf("rejected %d reservations", p.captureRejections.Load())
	}
	s.ReleaseCapture(64 << 20)
}

func TestCaptureReservationsRespectUnallocatedDiskSpace(t *testing.T) {
	p := newTestProvider(t, t.TempDir(), 0)
	p.limits.auto = true
	p.limits.maxDiskUsagePercent = 80
	p.diskUsage = func(string) (*disk.UsageStat, error) {
		return &disk.UsageStat{Total: 100 << 20, Used: 79 << 20}, nil
	}
	s := &Storage{provider: p}
	if !s.ReserveCapture(1 << 20) {
		t.Fatal("one MiB of free disk capacity was unavailable")
	}
	if s.ReserveCapture(1) {
		t.Fatal("unwritten reservation was treated as free disk space")
	}
	s.ReleaseCapture(1 << 20)
}
