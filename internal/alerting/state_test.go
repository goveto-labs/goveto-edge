package alerting

import (
	"testing"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

func TestDecideObservedTransitions(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		current      model.AlertStatus
		firstSeen    time.Time
		lastNotified *time.Time
		forDuration  time.Duration
		cooldown     time.Duration
		wantNext     model.AlertStatus
		wantNotify   bool
		wantChanged  bool
	}{
		{
			name: "pending below for-duration stays pending", current: model.AlertStatusPENDING,
			firstSeen: now.Add(-time.Minute), forDuration: 5 * time.Minute,
			wantNext: model.AlertStatusPENDING,
		},
		{
			name: "pending at for-duration fires", current: model.AlertStatusPENDING,
			firstSeen: now.Add(-5 * time.Minute), forDuration: 5 * time.Minute,
			wantNext: model.AlertStatusFIRING, wantNotify: true, wantChanged: true,
		},
		{
			name: "pending with zero for-duration fires immediately", current: model.AlertStatusPENDING,
			firstSeen: now, forDuration: 0,
			wantNext: model.AlertStatusFIRING, wantNotify: true, wantChanged: true,
		},
		{
			name: "firing with never-notified notifies", current: model.AlertStatusFIRING,
			forDuration: time.Minute, cooldown: 10 * time.Minute,
			wantNext: model.AlertStatusFIRING, wantNotify: true,
		},
		{
			name: "firing within cooldown stays quiet", current: model.AlertStatusFIRING,
			lastNotified: ptrTime(now.Add(-time.Minute)), cooldown: 10 * time.Minute,
			wantNext: model.AlertStatusFIRING,
		},
		{
			name: "firing past cooldown repeats reminder", current: model.AlertStatusFIRING,
			lastNotified: ptrTime(now.Add(-11 * time.Minute)), cooldown: 10 * time.Minute,
			wantNext: model.AlertStatusFIRING, wantNotify: true,
		},
		{
			name: "acknowledged never notifies", current: model.AlertStatusACKNOWLEDGED,
			lastNotified: ptrTime(now.Add(-time.Hour)), cooldown: time.Minute,
			wantNext: model.AlertStatusACKNOWLEDGED,
		},
		{
			name: "resolved is untouched", current: model.AlertStatusRESOLVED,
			wantNext: model.AlertStatusRESOLVED,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := decideObserved(test.current, test.firstSeen, test.lastNotified, test.forDuration, test.cooldown, now)
			if got.Next != test.wantNext || got.Notify != test.wantNotify || got.Changed != test.wantChanged {
				t.Fatalf("decideObserved = %+v, want next=%s notify=%t changed=%t",
					got, test.wantNext, test.wantNotify, test.wantChanged)
			}
		})
	}
}

func TestDecideMissingTransitions(t *testing.T) {
	tests := []struct {
		current       model.AlertStatus
		wantNext      model.AlertStatus
		wantRecovered bool
	}{
		{model.AlertStatusPENDING, model.AlertStatusRESOLVED, false},
		{model.AlertStatusFIRING, model.AlertStatusRESOLVED, true},
		{model.AlertStatusACKNOWLEDGED, model.AlertStatusRESOLVED, true},
		{model.AlertStatusRESOLVED, model.AlertStatusRESOLVED, false},
	}
	for _, test := range tests {
		next, recovered := decideMissing(test.current)
		if next != test.wantNext || recovered != test.wantRecovered {
			t.Fatalf("decideMissing(%s) = (%s, %t), want (%s, %t)",
				test.current, next, recovered, test.wantNext, test.wantRecovered)
		}
	}
}

func TestManualTransitions(t *testing.T) {
	if _, err := ValidateManualTransition(model.AlertStatusPENDING, "ack"); err == nil {
		t.Fatal("acknowledging a pending alert must be rejected")
	}
	if _, err := ValidateManualTransition(model.AlertStatusFIRING, "ack"); err != nil {
		t.Fatalf("acknowledging a firing alert failed: %v", err)
	}
	if _, err := ValidateManualTransition(model.AlertStatusRESOLVED, "ack"); err == nil {
		t.Fatal("acknowledging a resolved alert must be rejected")
	}
	if _, err := ValidateManualTransition(model.AlertStatusACKNOWLEDGED, "ack"); err != nil {
		t.Fatalf("re-acknowledging must be allowed: %v", err)
	}
	for _, status := range []model.AlertStatus{
		model.AlertStatusPENDING, model.AlertStatusFIRING, model.AlertStatusACKNOWLEDGED,
	} {
		if _, err := ValidateManualTransition(status, "resolve"); err != nil {
			t.Fatalf("resolving %s failed: %v", status, err)
		}
	}
	if _, err := ValidateManualTransition(model.AlertStatusRESOLVED, "resolve"); err == nil {
		t.Fatal("resolving an already resolved alert must be rejected")
	}
	if _, err := ValidateManualTransition(model.AlertStatusFIRING, "escalate"); err == nil {
		t.Fatal("unknown manual action must be rejected")
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

func TestDeliveryBackoffSchedule(t *testing.T) {
	want := []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute}
	for index, duration := range want {
		if got := backoff(index + 1); got != duration {
			t.Errorf("backoff(%d) = %s, want %s", index+1, got, duration)
		}
	}
}
