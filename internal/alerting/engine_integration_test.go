package alerting

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"goveto-edge/internal/node"
	"goveto-edge/internal/notify"
	"goveto-edge/internal/storage"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
	"goveto-edge/schema"
)

// TestEngineLifecycleIntegration walks the full alert lifecycle against a real
// PostgreSQL: seed -> pending -> firing (with delivery) -> acknowledge ->
// recovery -> resolved. Gated on GOVETO_TEST_DATABASE_URL.
func TestEngineLifecycleIntegration(t *testing.T) {
	databaseURL := strings.TrimSpace(getenvDefault("GOVETO_TEST_DATABASE_URL", ""))
	if databaseURL == "" {
		t.Skip("GOVETO_TEST_DATABASE_URL not set; skipping alerting lifecycle test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db, orm, err := storage.OpenPostgreSQL(ctx, databaseURL)
	if err != nil {
		t.Fatalf("configured test database is unreachable: %v", err)
	}
	defer db.Close()
	defer orm.Close()
	if _, err = storage.InitSchema(ctx, db, schema.FS, databaseURL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	user, err := orm.User.Create().Set(
		query.User.Email.Set("alerting-test-"+suffix+"@example.invalid"),
		query.User.PasswordHash.Set("x"),
		query.User.Name.Set("Alerting Test"),
	).Do(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	cluster, err := orm.Cluster.Create().Set(
		query.Cluster.CreatorId.Set(user.Id),
		query.Cluster.Name.Set("alerting-test-"+suffix),
	).Do(ctx)
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		// Nodes do not cascade from clusters. Alert instances now do.
		_, _ = orm.Node.DeleteMany(cleanup, query.Node.ClusterId.Equals(cluster.Id))
		_, _ = orm.Cluster.DeleteMany(cleanup, query.Cluster.Id.Equals(cluster.Id))
		_, _ = orm.User.DeleteMany(cleanup, query.User.Id.Equals(user.Id))
	})

	masterKeyMaterial := "integration-test-master-key!!!!!"
	if len(masterKeyMaterial) != 32 {
		t.Fatal("test master key must be 32 bytes")
	}
	cipher, err := node.NewCredentialCipherKeyring(base64.StdEncoding.EncodeToString([]byte(masterKeyMaterial)))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	channelID := newUUID()
	encrypted, err := cipher.EncryptScoped(notify.ChannelScope(cluster.Id, channelID), "logger://")
	if err != nil {
		t.Fatalf("encrypt channel url: %v", err)
	}
	if _, err = orm.NotificationChannel.Create().Set(
		query.NotificationChannel.Id.Set(channelID),
		query.NotificationChannel.ClusterId.Set(cluster.Id),
		query.NotificationChannel.Name.Set("test-logger"),
		query.NotificationChannel.Service.Set("logger"),
		query.NotificationChannel.UrlEncrypted.Set(encrypted),
		query.NotificationChannel.Enabled.Set(true),
	).Do(ctx); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	engine := New(orm, nil, cipher, time.Hour)
	var sendMu sync.Mutex
	var sent []string
	engine.send = func(_ context.Context, _ string, message notify.Message) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		sent = append(sent, message.Title)
		return nil
	}

	if err = engine.SeedDefaultRules(ctx); err != nil {
		t.Fatalf("seed rules: %v", err)
	}
	rule, err := orm.AlertRule.FindFirst(ctx,
		query.AlertRule.ClusterId.Equals(cluster.Id),
		query.AlertRule.Kind.Equals(KindNodeOffline),
	)
	if err != nil || rule == nil {
		t.Fatalf("node_offline rule missing after seed: %v", err)
	}
	originRule, err := orm.AlertRule.FindFirst(ctx,
		query.AlertRule.ClusterId.Equals(cluster.Id),
		query.AlertRule.Kind.Equals(KindOriginErrorRate),
	)
	if err != nil || originRule == nil || string(originRule.ParamsJson) != `{}` {
		t.Fatalf("new rules must store sparse params, rule=%+v err=%v", originRule, err)
	}

	nodeRow, err := orm.Node.Create().Set(
		query.Node.Id.Set(newUUID()),
		query.Node.ClusterId.Set(cluster.Id),
		query.Node.Name.Set("offline-node"),
		query.Node.Status.Set(model.NodeStatusOFFLINE),
	).Do(ctx)
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	// First tick: node_offline has forSeconds=0, so the instance fires
	// immediately and one delivery is enqueued per channel.
	engine.Tick(ctx)
	instance := findInstance(t, orm, cluster.Id, KindNodeOffline, nodeRow.Id)
	if instance.Status != model.AlertStatusFIRING {
		t.Fatalf("instance status = %s, want FIRING", instance.Status)
	}
	if instance.FiredAt == nil {
		t.Fatal("fired_at is not set")
	}
	deliveries := listDeliveries(t, orm, instance.Id)
	if len(deliveries) != 1 || deliveries[0].Kind != "firing" {
		t.Fatalf("firing deliveries = %+v, want one firing delivery", deliveries)
	}
	if deliveries[0].Status != model.AlertDeliveryStatusSENT {
		t.Fatalf("delivery status = %s, want SENT", deliveries[0].Status)
	}

	// Acknowledge.
	acked, err := Acknowledge(ctx, orm, cluster.Id, instance.Id, user.Id)
	if err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if acked.Status != model.AlertStatusACKNOWLEDGED || acked.AckedBy == nil || *acked.AckedBy != user.Id {
		t.Fatalf("ack result = %+v", acked)
	}
	ackEvents, err := orm.AlertEvent.Query().Where(
		query.AlertEvent.InstanceId.Equals(instance.Id),
		query.AlertEvent.Type.Equals(model.AlertEventTypeACK),
	).Do(ctx)
	if err != nil || len(ackEvents) != 1 {
		t.Fatalf("ack events = %+v err=%v, want 1", ackEvents, err)
	}
	// Acknowledging twice is allowed (reassignment), resolving is idempotent-rejected later.
	if _, err = Acknowledge(ctx, orm, cluster.Id, instance.Id, user.Id); err != nil {
		t.Fatalf("re-acknowledge: %v", err)
	}

	// Node returns: the alert resolves with a recovery notification.
	if _, err = orm.Node.Update().
		Where(query.Node.Id.Equals(nodeRow.Id)).
		Set(query.Node.Status.Set(model.NodeStatusONLINE)).Do(ctx); err != nil {
		t.Fatalf("revive node: %v", err)
	}
	engine.Tick(ctx)
	instance = findInstance(t, orm, cluster.Id, KindNodeOffline, nodeRow.Id)
	if instance.Status != model.AlertStatusRESOLVED {
		t.Fatalf("instance status after recovery = %s, want RESOLVED", instance.Status)
	}
	if instance.ResolvedReason == nil || *instance.ResolvedReason == "" {
		t.Fatal("resolved_reason is empty")
	}
	recovery, err := orm.AlertDelivery.Query().Where(
		query.AlertDelivery.InstanceId.Equals(instance.Id),
		query.AlertDelivery.Kind.Equals("recovery"),
	).Do(ctx)
	if err != nil {
		t.Fatalf("load recovery deliveries: %v", err)
	}
	if len(recovery) != 1 || recovery[0].Status != model.AlertDeliveryStatusSENT {
		t.Fatalf("recovery deliveries = %+v", recovery)
	}
	sendMu.Lock()
	sentCount := len(sent)
	sentSnapshot := append([]string(nil), sent...)
	sendMu.Unlock()
	if sentCount != 2 {
		t.Fatalf("sent notifications = %d, want 2 (firing + recovery): %v", sentCount, sentSnapshot)
	}

	// Manual resolve on a fresh alert.
	if _, err = orm.Node.Update().Where(query.Node.Id.Equals(nodeRow.Id)).
		Set(query.Node.Status.Set(model.NodeStatusOFFLINE)).Do(ctx); err != nil {
		t.Fatal(err)
	}
	engine.Tick(ctx)
	instance = findInstance(t, orm, cluster.Id, KindNodeOffline, nodeRow.Id)
	if instance.Status != model.AlertStatusFIRING {
		t.Fatalf("instance status after re-offline = %s, want FIRING", instance.Status)
	}
	resolved, err := Resolve(ctx, orm, cluster.Id, instance.Id, "fixed in maintenance window")
	if err != nil {
		t.Fatalf("manual resolve: %v", err)
	}
	if resolved.Status != model.AlertStatusRESOLVED || resolved.ResolvedReason == nil ||
		*resolved.ResolvedReason != "fixed in maintenance window" {
		t.Fatalf("resolve result = %+v", resolved)
	}
	if _, err = Resolve(ctx, orm, cluster.Id, instance.Id, "again"); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("resolving a resolved alert should conflict, got %v", err)
	}
	manuallyResolvedID := instance.Id

	// The observed condition still exists, so the engine must neither revive
	// the resolved row nor create a replacement yet.
	engine.Tick(ctx)
	resolvedRows, err := orm.AlertInstance.Query().Where(
		query.AlertInstance.ClusterId.Equals(cluster.Id),
		query.AlertInstance.Kind.Equals(KindNodeOffline),
		query.AlertInstance.Fingerprint.Equals(Fingerprint(KindNodeOffline, nodeRow.Id)),
	).Do(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range resolvedRows {
		if row.Id == manuallyResolvedID && row.Status != model.AlertStatusRESOLVED {
			t.Fatalf("manually resolved instance was revived as %s", row.Status)
		}
		if row.Id != manuallyResolvedID && row.Status != model.AlertStatusRESOLVED {
			t.Fatalf("condition persistence created active replacement %s", row.Id)
		}
	}

	// Clearing the condition rearms the fingerprint. A later recurrence must
	// create a new active instance rather than resurrecting the old ID.
	if _, err = orm.Node.Update().Where(query.Node.Id.Equals(nodeRow.Id)).
		Set(query.Node.Status.Set(model.NodeStatusONLINE)).Do(ctx); err != nil {
		t.Fatal(err)
	}
	engine.Tick(ctx)
	rearmEvents, err := orm.AlertEvent.Query().Where(
		query.AlertEvent.InstanceId.Equals(manuallyResolvedID),
		query.AlertEvent.Type.Equals(model.AlertEventTypeREARM),
	).Do(ctx)
	if err != nil || len(rearmEvents) != 1 {
		t.Fatalf("rearm events = %+v err=%v, want 1", rearmEvents, err)
	}
	if _, err = orm.Node.Update().Where(query.Node.Id.Equals(nodeRow.Id)).
		Set(query.Node.Status.Set(model.NodeStatusOFFLINE)).Do(ctx); err != nil {
		t.Fatal(err)
	}
	engine.Tick(ctx)
	recreated := findInstance(t, orm, cluster.Id, KindNodeOffline, nodeRow.Id)
	if recreated.Id == manuallyResolvedID || recreated.Status != model.AlertStatusFIRING {
		t.Fatalf("recurrence instance = %s/%s, want new FIRING instance", recreated.Id, recreated.Status)
	}
	_, duplicateErr := orm.AlertInstance.Create().Set(
		query.AlertInstance.Id.Set(newUUID()),
		query.AlertInstance.RuleId.Set(recreated.RuleId),
		query.AlertInstance.ClusterId.Set(cluster.Id),
		query.AlertInstance.Kind.Set(recreated.Kind),
		query.AlertInstance.Fingerprint.Set(recreated.Fingerprint),
		query.AlertInstance.Status.Set(model.AlertStatusPENDING),
		query.AlertInstance.Severity.Set(recreated.Severity),
		query.AlertInstance.Title.Set("duplicate active instance"),
		query.AlertInstance.DetailJson.Set([]byte(`{}`)),
		query.AlertInstance.FirstSeenAt.Set(time.Now()),
		query.AlertInstance.LastSeenAt.Set(time.Now()),
	).Do(ctx)
	if duplicateErr == nil {
		t.Fatal("active fingerprint unique constraint accepted a duplicate")
	}
	var postgresError *pgconn.PgError
	if !errors.As(duplicateErr, &postgresError) || postgresError.Code != "23505" {
		t.Fatalf("duplicate error = %v, want PostgreSQL 23505", duplicateErr)
	}

	// Simulate an upgrade from the pre-constraint schema. Startup must resolve
	// legacy duplicates and recreate the invariant with a concurrent index.
	if _, err = orm.RawExec(ctx, `DROP INDEX alert_instances_active_fingerprint_key`); err != nil {
		t.Fatal(err)
	}
	if _, err = orm.AlertInstance.Create().Set(
		query.AlertInstance.Id.Set(newUUID()),
		query.AlertInstance.RuleId.Set(recreated.RuleId),
		query.AlertInstance.ClusterId.Set(cluster.Id),
		query.AlertInstance.Kind.Set(recreated.Kind),
		query.AlertInstance.Fingerprint.Set(recreated.Fingerprint),
		query.AlertInstance.Status.Set(model.AlertStatusPENDING),
		query.AlertInstance.Severity.Set(recreated.Severity),
		query.AlertInstance.Title.Set("legacy duplicate active instance"),
		query.AlertInstance.DetailJson.Set([]byte(`{}`)),
		query.AlertInstance.FirstSeenAt.Set(time.Now()),
		query.AlertInstance.LastSeenAt.Set(time.Now()),
	).Do(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = storage.InitSchema(ctx, db, schema.FS, databaseURL); err != nil {
		t.Fatalf("repair duplicate active instances during schema upgrade: %v", err)
	}
	activeCount, err := orm.AlertInstance.Query().Where(
		query.AlertInstance.RuleId.Equals(recreated.RuleId),
		query.AlertInstance.Fingerprint.Equals(recreated.Fingerprint),
		query.AlertInstance.Status.Not(model.AlertStatusRESOLVED),
	).Count(ctx)
	if err != nil || activeCount != 1 {
		t.Fatalf("active duplicates after upgrade = %d err=%v, want 1", activeCount, err)
	}

	events, err := orm.AlertEvent.Query().
		Where(query.AlertEvent.InstanceId.Equals(instance.Id)).
		OrderBy(query.AlertEvent.CreatedAt.Asc()).Do(ctx)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	var sawStateChange, sawResolve bool
	for _, event := range events {
		switch event.Type {
		case model.AlertEventTypeSTATE_CHANGE:
			sawStateChange = true
		case model.AlertEventTypeRESOLVE:
			sawResolve = true
		}
	}
	if !sawStateChange || !sawResolve {
		t.Fatalf("event timeline incomplete: state_change=%t resolve=%t", sawStateChange, sawResolve)
	}

	// Pending-only alerts resolve silently when the condition clears before
	// firing (node_redis_unavailable has forSeconds=60).
	redisRule, err := orm.AlertRule.FindFirst(ctx,
		query.AlertRule.ClusterId.Equals(cluster.Id),
		query.AlertRule.Kind.Equals(KindNodeRedisDown),
	)
	if err != nil || redisRule == nil {
		t.Fatalf("redis rule: %v", err)
	}
	if _, err = orm.AlertRule.Update().Where(query.AlertRule.Id.Equals(redisRule.Id)).
		Set(query.AlertRule.ParamsJson.Set([]byte(`{"legacy_invalid":-1}`))).Do(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = orm.Node.Update().Where(query.Node.Id.Equals(nodeRow.Id)).
		Set(query.Node.Status.Set(model.NodeStatusONLINE), query.Node.RedisAvailable.Set(false)).Do(ctx); err != nil {
		t.Fatal(err)
	}
	engine.Tick(ctx)
	pending := findInstance(t, orm, cluster.Id, KindNodeRedisDown, nodeRow.Id)
	if pending.Status != model.AlertStatusPENDING {
		t.Fatalf("redis instance status = %s, want PENDING (for-duration not yet reached)", pending.Status)
	}
	redisRule, err = orm.AlertRule.FindUnique(ctx, query.AlertRule.Id.Equals(redisRule.Id))
	if err != nil || redisRule == nil || string(redisRule.ParamsJson) != `{}` {
		t.Fatalf("legacy params were not compacted: rule=%+v err=%v", redisRule, err)
	}
	sendMu.Lock()
	before := len(sent)
	sendMu.Unlock()
	if _, err = orm.Node.Update().Where(query.Node.Id.Equals(nodeRow.Id)).
		Set(query.Node.RedisAvailable.Set(true)).Do(ctx); err != nil {
		t.Fatal(err)
	}
	engine.Tick(ctx)
	pending = findInstance(t, orm, cluster.Id, KindNodeRedisDown, nodeRow.Id)
	if pending.Status != model.AlertStatusRESOLVED {
		t.Fatalf("redis instance status = %s, want RESOLVED", pending.Status)
	}
	if _, err = Acknowledge(ctx, orm, cluster.Id, pending.Id, user.Id); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("acknowledging resolved alert error = %v, want ErrIllegalTransition", err)
	}
	sendMu.Lock()
	after := len(sent)
	sendMu.Unlock()
	if after != before {
		t.Fatalf("pending alert emitted %d notifications on silent resolve, want 0", after-before)
	}

	// Muting suppresses delivery only: state continues to FIRING.
	mutedUntil := time.Now().Add(time.Hour)
	if _, err = orm.AlertRule.Update().Where(query.AlertRule.Id.Equals(rule.Id)).
		Set(query.AlertRule.MutedUntil.Set(mutedUntil)).Do(ctx); err != nil {
		t.Fatal(err)
	}
	mutedNode, err := orm.Node.Create().Set(
		query.Node.Id.Set(newUUID()),
		query.Node.ClusterId.Set(cluster.Id),
		query.Node.Name.Set("muted-offline-node"),
		query.Node.Status.Set(model.NodeStatusOFFLINE),
	).Do(ctx)
	if err != nil {
		t.Fatal(err)
	}
	engine.Tick(ctx)
	mutedInstance := findInstance(t, orm, cluster.Id, KindNodeOffline, mutedNode.Id)
	if mutedInstance.Status != model.AlertStatusFIRING {
		t.Fatalf("muted instance status = %s, want FIRING", mutedInstance.Status)
	}
	if got := listDeliveries(t, orm, mutedInstance.Id); len(got) != 0 {
		t.Fatalf("muted rule created deliveries: %+v", got)
	}
	if _, err = orm.AlertRule.Update().Where(query.AlertRule.Id.Equals(rule.Id)).
		Set(query.AlertRule.MutedUntil.SetNull()).Do(ctx); err != nil {
		t.Fatal(err)
	}

	// Invalid channel JSON advances state but must not write notification
	// bookkeeping. Fixing the config should notify immediately.
	if _, err = orm.AlertRule.Update().Where(query.AlertRule.Id.Equals(rule.Id)).
		Set(query.AlertRule.ChannelsJson.Set([]byte(`{}`))).Do(ctx); err != nil {
		t.Fatal(err)
	}
	badChannelNode, err := orm.Node.Create().Set(
		query.Node.Id.Set(newUUID()), query.Node.ClusterId.Set(cluster.Id),
		query.Node.Name.Set("bad-channel-node"), query.Node.Status.Set(model.NodeStatusOFFLINE),
	).Do(ctx)
	if err != nil {
		t.Fatal(err)
	}
	engine.Tick(ctx)
	badChannelInstance := findInstance(t, orm, cluster.Id, KindNodeOffline, badChannelNode.Id)
	if badChannelInstance.Status != model.AlertStatusFIRING || badChannelInstance.LastNotifiedAt != nil || badChannelInstance.NotifyCount != 0 {
		t.Fatalf("bad-channel instance bookkeeping = %+v", badChannelInstance)
	}
	if _, err = orm.AlertRule.Update().Where(query.AlertRule.Id.Equals(rule.Id)).
		Set(query.AlertRule.ChannelsJson.Set([]byte(`[]`))).Do(ctx); err != nil {
		t.Fatal(err)
	}
	engine.Tick(ctx)
	badChannelInstance = findInstance(t, orm, cluster.Id, KindNodeOffline, badChannelNode.Id)
	if badChannelInstance.LastNotifiedAt == nil || badChannelInstance.NotifyCount == 0 {
		t.Fatalf("fixed channel config did not notify immediately: %+v", badChannelInstance)
	}

	// Missing analytics must preserve metric-backed active state.
	resourceRule, err := orm.AlertRule.FindFirst(ctx,
		query.AlertRule.ClusterId.Equals(cluster.Id),
		query.AlertRule.Kind.Equals(KindNodeResourceUsage),
	)
	if err != nil || resourceRule == nil {
		t.Fatalf("resource rule: %v", err)
	}
	metricInstance, err := orm.AlertInstance.Create().Set(
		query.AlertInstance.Id.Set(newUUID()),
		query.AlertInstance.RuleId.Set(resourceRule.Id),
		query.AlertInstance.ClusterId.Set(cluster.Id),
		query.AlertInstance.Kind.Set(KindNodeResourceUsage),
		query.AlertInstance.Fingerprint.Set(Fingerprint(KindNodeResourceUsage, nodeRow.Id)),
		query.AlertInstance.Status.Set(model.AlertStatusFIRING),
		query.AlertInstance.Severity.Set(resourceRule.Severity),
		query.AlertInstance.Title.Set("resource usage"),
		query.AlertInstance.DetailJson.Set([]byte(`{}`)),
		query.AlertInstance.FirstSeenAt.Set(time.Now()),
		query.AlertInstance.LastSeenAt.Set(time.Now()),
	).Do(ctx)
	if err != nil {
		t.Fatal(err)
	}
	engine.Tick(ctx)
	metricInstance, err = orm.AlertInstance.FindUnique(ctx, query.AlertInstance.Id.Equals(metricInstance.Id))
	if err != nil || metricInstance == nil || metricInstance.Status != model.AlertStatusFIRING {
		t.Fatalf("analytics-unavailable instance = %+v err=%v, want FIRING", metricInstance, err)
	}

	// Retention deletes ordinary resolved history but retains manually
	// suppressed rows until the condition is observed clear.
	oldResolvedAt := time.Now().Add(-resolvedRetention - time.Hour)
	retainedIDs := make([]string, 0, 2)
	for _, suppressed := range []bool{false, true} {
		created, createErr := orm.AlertInstance.Create().Set(
			query.AlertInstance.Id.Set(newUUID()),
			query.AlertInstance.RuleId.Set(resourceRule.Id),
			query.AlertInstance.ClusterId.Set(cluster.Id),
			query.AlertInstance.Kind.Set(KindNodeResourceUsage),
			query.AlertInstance.Fingerprint.Set(Fingerprint(KindNodeResourceUsage, newUUID())),
			query.AlertInstance.Status.Set(model.AlertStatusRESOLVED),
			query.AlertInstance.Severity.Set(resourceRule.Severity),
			query.AlertInstance.Title.Set("old resolved alert"),
			query.AlertInstance.DetailJson.Set([]byte(`{}`)),
			query.AlertInstance.FirstSeenAt.Set(oldResolvedAt),
			query.AlertInstance.LastSeenAt.Set(oldResolvedAt),
			query.AlertInstance.ResolvedAt.Set(oldResolvedAt),
			query.AlertInstance.SuppressedUntilClear.Set(suppressed),
		).Do(ctx)
		if createErr != nil {
			t.Fatal(createErr)
		}
		retainedIDs = append(retainedIDs, created.Id)
	}
	engine.lastSweep = time.Time{}
	engine.sweepHistory(ctx)
	deleted, err := orm.AlertInstance.FindUnique(ctx, query.AlertInstance.Id.Equals(retainedIDs[0]))
	if err != nil || deleted != nil {
		t.Fatalf("ordinary expired history still exists: %+v err=%v", deleted, err)
	}
	retained, err := orm.AlertInstance.FindUnique(ctx, query.AlertInstance.Id.Equals(retainedIDs[1]))
	if err != nil || retained == nil {
		t.Fatalf("suppressed history was swept: %+v err=%v", retained, err)
	}

	// Delivery failures retain only a safe error category, increment attempts
	// after a completed attempt, and become terminal on the fifth failure.
	failingDelivery, err := orm.AlertDelivery.Create().Set(
		query.AlertDelivery.InstanceId.Set(pending.Id),
		query.AlertDelivery.ChannelId.Set(channelID),
		query.AlertDelivery.Kind.Set("firing"),
		query.AlertDelivery.Status.Set(model.AlertDeliveryStatusPENDING),
		query.AlertDelivery.Attempts.Set(0),
	).Do(ctx)
	if err != nil {
		t.Fatal(err)
	}
	engine.send = func(context.Context, string, notify.Message) error {
		return fmt.Errorf("queue full: %w", notify.ErrSendCongestion)
	}
	engine.deliverOne(ctx, failingDelivery.Id, 1)
	failingDelivery, err = orm.AlertDelivery.FindUnique(ctx, query.AlertDelivery.Id.Equals(failingDelivery.Id))
	if err != nil || failingDelivery.Attempts != 0 || failingDelivery.Status != model.AlertDeliveryStatusPENDING || failingDelivery.LastError != nil {
		t.Fatalf("congestion consumed delivery attempt: delivery=%+v err=%v", failingDelivery, err)
	}
	secretURL := "https://gotify.example.invalid/message?token=integration-secret"
	engine.send = func(context.Context, string, notify.Message) error {
		return &url.Error{Op: "Post", URL: secretURL, Err: errors.New("connection refused")}
	}
	engine.deliverOne(ctx, failingDelivery.Id, 1)
	failingDelivery, err = orm.AlertDelivery.FindUnique(ctx, query.AlertDelivery.Id.Equals(failingDelivery.Id))
	if err != nil {
		t.Fatal(err)
	}
	if failingDelivery.Attempts != 1 || failingDelivery.Status != model.AlertDeliveryStatusPENDING || failingDelivery.NextRetryAt == nil {
		t.Fatalf("first failed delivery = %+v", failingDelivery)
	}
	if failingDelivery.LastError == nil || strings.Contains(*failingDelivery.LastError, "integration-secret") || strings.Contains(*failingDelivery.LastError, secretURL) {
		t.Fatalf("stored delivery error leaked credentials: %v", failingDelivery.LastError)
	}
	engine.deliverOne(ctx, failingDelivery.Id, deliveryMaxAttempts)
	failingDelivery, err = orm.AlertDelivery.FindUnique(ctx, query.AlertDelivery.Id.Equals(failingDelivery.Id))
	if err != nil {
		t.Fatal(err)
	}
	if failingDelivery.Attempts != deliveryMaxAttempts || failingDelivery.Status != model.AlertDeliveryStatusFAILED || failingDelivery.NextRetryAt != nil {
		t.Fatalf("terminal failed delivery = %+v", failingDelivery)
	}

	disabledDelivery, err := orm.AlertDelivery.Create().Set(
		query.AlertDelivery.InstanceId.Set(pending.Id),
		query.AlertDelivery.ChannelId.Set(channelID),
		query.AlertDelivery.Kind.Set("firing"),
		query.AlertDelivery.Status.Set(model.AlertDeliveryStatusPENDING),
		query.AlertDelivery.Attempts.Set(0),
	).Do(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = orm.NotificationChannel.Update().Where(query.NotificationChannel.Id.Equals(channelID)).
		Set(query.NotificationChannel.Enabled.Set(false)).Do(ctx); err != nil {
		t.Fatal(err)
	}
	engine.deliverOne(ctx, disabledDelivery.Id, 1)
	disabledDelivery, err = orm.AlertDelivery.FindUnique(ctx, query.AlertDelivery.Id.Equals(disabledDelivery.Id))
	if err != nil {
		t.Fatal(err)
	}
	if disabledDelivery.Status != model.AlertDeliveryStatusFAILED || disabledDelivery.Attempts != 1 {
		t.Fatalf("disabled-channel delivery = %+v, want permanent failure", disabledDelivery)
	}
}

func findInstance(t *testing.T, orm *client.Client, clusterID, kind, resourceID string) *model.AlertInstance {
	t.Helper()
	instance, err := orm.AlertInstance.FindFirst(ctxless(),
		query.AlertInstance.ClusterId.Equals(clusterID),
		query.AlertInstance.Kind.Equals(kind),
		query.AlertInstance.Fingerprint.Equals(Fingerprint(kind, resourceID)),
		query.AlertInstance.Status.Not(model.AlertStatusRESOLVED),
	)
	if err != nil {
		t.Fatalf("find %s instance: %v", kind, err)
	}
	if instance == nil {
		instance, err = orm.AlertInstance.FindFirst(ctxless(),
			query.AlertInstance.ClusterId.Equals(clusterID),
			query.AlertInstance.Kind.Equals(kind),
			query.AlertInstance.Fingerprint.Equals(Fingerprint(kind, resourceID)),
		)
		if err != nil {
			t.Fatalf("find %s instance (history): %v", kind, err)
		}
		if instance == nil {
			t.Fatalf("%s instance for resource %s not found", kind, resourceID)
		}
	}
	return instance
}

func listDeliveries(t *testing.T, orm *client.Client, instanceID string) []model.AlertDelivery {
	t.Helper()
	deliveries, err := orm.AlertDelivery.Query().
		Where(query.AlertDelivery.InstanceId.Equals(instanceID)).
		OrderBy(query.AlertDelivery.CreatedAt.Asc()).Do(ctxless())
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	return deliveries
}

func ctxless() context.Context {
	return context.Background()
}

func getenvDefault(key, fallback string) string {
	if value := strings.TrimSpace(osGetenv(key)); value != "" {
		return value
	}
	return fallback
}

func osGetenv(key string) string { return os.Getenv(key) }

func newUUID() string { return uuid.NewString() }
