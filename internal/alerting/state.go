// Package alerting implements the cluster-scoped alert state machine and the
// evaluation engine that turns control-plane signals (node health, agent log
// queues, certificates, jobs, analytics metrics) into alert instances and
// notification deliveries.
//
// State machine (the only legal transitions):
//
//	PENDING        -> FIRING        condition held for the rule's for-duration
//	PENDING        -> RESOLVED      condition cleared before firing (silent)
//	FIRING         -> ACKNOWLEDGED  operator acknowledgement
//	FIRING         -> RESOLVED      condition cleared (recovery notification)
//	ACKNOWLEDGED   -> RESOLVED      condition cleared (recovery notification)
//	PENDING/FIRING/ACKNOWLEDGED -> RESOLVED  manual resolve
//
// Acknowledgement is a marker only: an acknowledged alert never transitions
// back to FIRING and stops reminder notifications while the condition holds.
// Re-occurrence after resolution creates a fresh instance (fingerprint dedup
// only reuses instances that are not RESOLVED).
package alerting

import (
	"errors"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

// ErrIllegalTransition is returned for state changes outside the table above.
var ErrIllegalTransition = errors.New("illegal alert state transition")

// observedDecision is what the engine does for one instance whose condition
// is currently present.
type observedDecision struct {
	Next    model.AlertStatus
	Notify  bool // send (or repeat) the firing notification
	Changed bool // the status column changes
}

// decideObserved evaluates an instance whose condition is present. forDuration
// is the rule's pending->firing hold time; cooldown bounds reminder repeats
// while the alert keeps firing. Acknowledged instances never notify again.
func decideObserved(
	current model.AlertStatus,
	firstSeen time.Time,
	lastNotified *time.Time,
	forDuration, cooldown time.Duration,
	now time.Time,
) observedDecision {
	switch current {
	case model.AlertStatusPENDING:
		if now.Sub(firstSeen) >= forDuration {
			return observedDecision{Next: model.AlertStatusFIRING, Notify: true, Changed: true}
		}
		return observedDecision{Next: model.AlertStatusPENDING}
	case model.AlertStatusFIRING:
		notify := lastNotified == nil || now.Sub(*lastNotified) >= cooldown
		return observedDecision{Next: model.AlertStatusFIRING, Notify: notify}
	case model.AlertStatusACKNOWLEDGED:
		return observedDecision{Next: model.AlertStatusACKNOWLEDGED}
	case model.AlertStatusRESOLVED:
		// Resolved instances are never updated in place; a re-occurrence
		// creates a new instance.
		return observedDecision{Next: model.AlertStatusRESOLVED}
	default:
		return observedDecision{Next: current}
	}
}

// decideMissing evaluates an instance whose condition is no longer present.
// Recovery notifications are only sent for instances that actually fired.
func decideMissing(current model.AlertStatus) (next model.AlertStatus, recovered bool) {
	switch current {
	case model.AlertStatusPENDING:
		return model.AlertStatusRESOLVED, false
	case model.AlertStatusFIRING:
		return model.AlertStatusRESOLVED, true
	case model.AlertStatusACKNOWLEDGED:
		return model.AlertStatusRESOLVED, true
	default:
		return current, false
	}
}

// CanAcknowledge reports whether an operator may acknowledge the instance.
// Acknowledging an already acknowledged instance is allowed so the ack can be
// reassigned, but pending or resolved instances cannot be acknowledged.
func CanAcknowledge(status model.AlertStatus) bool {
	return status == model.AlertStatusFIRING || status == model.AlertStatusACKNOWLEDGED
}

// CanResolve reports whether an operator may manually resolve the instance.
func CanResolve(status model.AlertStatus) bool {
	switch status {
	case model.AlertStatusPENDING, model.AlertStatusFIRING, model.AlertStatusACKNOWLEDGED:
		return true
	default:
		return false
	}
}

// ValidateManualTransition maps a manual action onto the target status.
func ValidateManualTransition(from model.AlertStatus, action string) (model.AlertStatus, error) {
	switch action {
	case "ack":
		if !CanAcknowledge(from) {
			return from, ErrIllegalTransition
		}
		return model.AlertStatusACKNOWLEDGED, nil
	case "resolve":
		if !CanResolve(from) {
			return from, ErrIllegalTransition
		}
		return model.AlertStatusRESOLVED, nil
	default:
		return from, ErrIllegalTransition
	}
}
