package analytics

import (
	"testing"
	"time"
)

func TestCompleteHourRange(t *testing.T) {
	from := time.Date(2026, 7, 29, 10, 15, 0, 0, time.UTC)
	to := time.Date(2026, 7, 29, 13, 45, 0, 0, time.UTC)
	start, end := completeHourRange(from, to)
	if !start.Equal(time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC)) ||
		!end.Equal(time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("complete range = %s to %s", start, end)
	}
}

func TestCompleteHourRangeWithoutFullHour(t *testing.T) {
	from := time.Date(2026, 7, 29, 10, 15, 0, 0, time.UTC)
	to := time.Date(2026, 7, 29, 10, 45, 0, 0, time.UTC)
	start, end := completeHourRange(from, to)
	if !start.Equal(to) || !end.Equal(to) {
		t.Fatalf("empty complete range = %s to %s", start, end)
	}
}

func TestCompleteHourRangeKeepsAlignedBoundary(t *testing.T) {
	from := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC)
	start, end := completeHourRange(from, to)
	if !start.Equal(from) || !end.Equal(to) {
		t.Fatalf("aligned complete range = %s to %s", start, end)
	}
}

func TestUTCDayRangeAlignsStartAndEnd(t *testing.T) {
	now := time.Date(2026, 7, 29, 7, 45, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	from, today := utcDayRange(now, 30)
	if !from.Equal(time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)) ||
		!today.Equal(time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("UTC day range = %s to %s", from, today)
	}
}
