package alerting

import (
	"context"
	"errors"
	"time"

	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// ErrAlertNotFound maps to HTTP 404, ErrIllegalTransition to 409.
var ErrAlertNotFound = errors.New("alert not found")

// Acknowledge marks a firing alert as acknowledged by uid. Re-acknowledging is
// allowed so ownership can be reassigned; pending or resolved alerts are
// rejected with ErrIllegalTransition.
func Acknowledge(ctx context.Context, db *client.Client, clusterID, alertID, userID string) (*model.AlertInstance, error) {
	return mutateInstance(ctx, db, clusterID, alertID, func(instance *model.AlertInstance) ([]query.AlertInstanceSetClause, model.AlertStatus, error) {
		if !CanAcknowledge(instance.Status) {
			return nil, instance.Status, ErrIllegalTransition
		}
		return []query.AlertInstanceSetClause{
			query.AlertInstance.Status.Set(model.AlertStatusACKNOWLEDGED),
			query.AlertInstance.AckedAt.Set(nowUTC()),
			query.AlertInstance.AckedBy.Set(userID),
		}, model.AlertStatusACKNOWLEDGED, nil
	}, model.AlertEventTypeACK, nil)
}

// Resolve closes an alert manually and suppresses the same fingerprint until
// the engine observes the condition clear. No recovery notification is sent.
func Resolve(ctx context.Context, db *client.Client, clusterID, alertID, reason string) (*model.AlertInstance, error) {
	return mutateInstance(ctx, db, clusterID, alertID, func(instance *model.AlertInstance) ([]query.AlertInstanceSetClause, model.AlertStatus, error) {
		if !CanResolve(instance.Status) {
			return nil, instance.Status, ErrIllegalTransition
		}
		if reason == "" {
			reason = "resolved manually"
		}
		return []query.AlertInstanceSetClause{
			query.AlertInstance.Status.Set(model.AlertStatusRESOLVED),
			query.AlertInstance.ResolvedAt.Set(nowUTC()),
			query.AlertInstance.ResolvedReason.Set(reason),
			query.AlertInstance.SuppressedUntilClear.Set(true),
		}, model.AlertStatusRESOLVED, nil
	}, model.AlertEventTypeRESOLVE, func(payload map[string]any) map[string]any {
		if payload == nil {
			payload = map[string]any{}
		}
		payload["manual"] = true
		return payload
	})
}

func mutateInstance(
	ctx context.Context,
	db *client.Client,
	clusterID, alertID string,
	mutate func(*model.AlertInstance) ([]query.AlertInstanceSetClause, model.AlertStatus, error),
	eventType model.AlertEventType,
	decorate func(map[string]any) map[string]any,
) (*model.AlertInstance, error) {
	var result *model.AlertInstance
	err := db.Tx(ctx, func(tx *client.Client) error {
		// Lock the instance so a concurrent engine tick cannot transition it
		// between the status check and the write.
		locked, err := client.Raw[struct {
			ID string `db:"id"`
		}](ctx, tx, `SELECT id FROM alert_instances WHERE id=$1 AND cluster_id=$2 FOR UPDATE`, alertID, clusterID)
		if err != nil {
			return err
		}
		if len(locked) != 1 {
			return ErrAlertNotFound
		}
		instance, err := tx.AlertInstance.FindUnique(ctx, query.AlertInstance.Id.Equals(alertID))
		if err != nil || instance == nil {
			if err == nil {
				err = ErrAlertNotFound
			}
			return err
		}
		sets, next, transitionErr := mutate(instance)
		if transitionErr != nil {
			return transitionErr
		}
		updated, err := tx.AlertInstance.Update().
			Where(query.AlertInstance.Id.Equals(instance.Id)).
			Set(sets...).Do(ctx)
		if err != nil {
			return err
		}
		result = updated
		recordTransition(string(instance.Status), string(next))
		var payload map[string]any
		if decorate != nil {
			payload = decorate(payload)
		}
		if _, err = recordEvent(ctx, tx, instance.Id, eventType,
			string(instance.Status), string(next), payload); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func nowUTC() time.Time { return time.Now().UTC() }
