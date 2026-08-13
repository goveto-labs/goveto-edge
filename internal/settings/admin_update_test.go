package settings

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"goveto-edge/internal/storage/gen/client"
)

type atomicUpdateCipher struct {
	encryptions int
}

func (c *atomicUpdateCipher) EncryptScoped(scope, value string) (string, error) {
	c.encryptions++
	return "encrypted:" + scope + ":" + value, nil
}

func (*atomicUpdateCipher) DecryptScoped(_, value string) (string, error) {
	return value, nil
}

type atomicUpdateDB struct {
	mu            sync.Mutex
	settings      map[string]string
	audits        int
	failUpsertAt  int
	beginCount    int
	commitCount   int
	rollbackCount int
	upsertCount   int
}

type atomicUpdateDriver struct {
	db *atomicUpdateDB
}

func (d *atomicUpdateDriver) Open(string) (driver.Conn, error) {
	return &atomicUpdateConn{db: d.db}, nil
}

type atomicUpdateConnector struct {
	driver *atomicUpdateDriver
}

func (c atomicUpdateConnector) Connect(context.Context) (driver.Conn, error) {
	return c.driver.Open("")
}

func (c atomicUpdateConnector) Driver() driver.Driver {
	return c.driver
}

type atomicUpdateConn struct {
	db *atomicUpdateDB
	tx *atomicUpdateTx
}

func (*atomicUpdateConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are unsupported")
}

func (*atomicUpdateConn) Close() error { return nil }

func (c *atomicUpdateConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *atomicUpdateConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	if c.tx != nil {
		return nil, errors.New("transaction already active")
	}
	c.db.beginCount++
	c.tx = &atomicUpdateTx{
		conn:     c,
		settings: maps.Clone(c.db.settings),
		audits:   c.db.audits,
	}
	return c.tx, nil
}

func (c *atomicUpdateConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.tx == nil {
		return nil, errors.New("query outside transaction")
	}
	switch {
	case strings.HasPrefix(query, "SELECT ") && strings.Contains(query, `FROM "dynamic_settings"`):
		key := args[0].Value.(string)
		value, found := c.tx.settings[key]
		if !found {
			return &atomicUpdateRows{columns: dynamicSettingColumns}, nil
		}
		return &atomicUpdateRows{
			columns: dynamicSettingColumns,
			values:  [][]driver.Value{{key, []byte(value), "old description", time.Unix(1, 0).UTC()}},
		}, nil
	case strings.HasPrefix(query, `INSERT INTO "dynamic_settings"`):
		c.db.mu.Lock()
		c.db.upsertCount++
		upsertCount := c.db.upsertCount
		c.db.mu.Unlock()
		if upsertCount == c.db.failUpsertAt {
			return nil, errors.New("injected setting write failure")
		}
		key := args[0].Value.(string)
		value := string(args[1].Value.([]byte))
		description := args[2].Value.(string)
		updatedAt := args[3].Value.(time.Time)
		c.tx.settings[key] = value
		return &atomicUpdateRows{
			columns: dynamicSettingColumns,
			values:  [][]driver.Value{{key, []byte(value), description, updatedAt}},
		}, nil
	case strings.HasPrefix(query, `INSERT INTO "audit_logs"`):
		c.tx.audits++
		return &atomicUpdateRows{
			columns: auditLogColumns,
			values: [][]driver.Value{{
				"audit-id", nil, "system", "internal", "goveto-edge", "system_setting.update",
				"dynamic_setting", "setting", nil, nil, "request-id", "SUCCESS", nil, time.Unix(1, 0).UTC(),
			}},
		}, nil
	default:
		return nil, errors.New("unexpected query: " + query)
	}
}

type atomicUpdateTx struct {
	conn     *atomicUpdateConn
	settings map[string]string
	audits   int
}

func (tx *atomicUpdateTx) Commit() error {
	tx.conn.db.mu.Lock()
	defer tx.conn.db.mu.Unlock()
	tx.conn.db.settings = maps.Clone(tx.settings)
	tx.conn.db.audits = tx.audits
	tx.conn.db.commitCount++
	tx.conn.tx = nil
	return nil
}

func (tx *atomicUpdateTx) Rollback() error {
	tx.conn.db.mu.Lock()
	defer tx.conn.db.mu.Unlock()
	tx.conn.db.rollbackCount++
	tx.conn.tx = nil
	return nil
}

type atomicUpdateRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *atomicUpdateRows) Columns() []string { return r.columns }
func (*atomicUpdateRows) Close() error        { return nil }

func (r *atomicUpdateRows) Next(destination []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(destination, r.values[r.index])
	r.index++
	return nil
}

var dynamicSettingColumns = []string{"key", "value_json", "description", "updated_at"}

var auditLogColumns = []string{
	"id", "actor_id", "actor", "source_ip", "user_agent", "action", "resource", "resource_id",
	"before_json", "after_json", "request_id", "result", "failure_reason", "created_at",
}

func TestApplyAdminSettingsUpdateRollsBackOnNthWriteFailure(t *testing.T) {
	initial := map[string]string{
		AgentGatewayAddressKey: `"old.example.com:8443"`,
		HTTPProxyKey:           `{"trust_all":false,"client_ip_headers":["X-Forwarded-For"]}`,
		LocalLoginEnabledKey:   `true`,
		JobRetentionKey:        `{"history_days":90,"versions_per_site":20}`,
		RequireTOTPKey:         `false`,
		AuthProvidersKey:       `[]`,
		RegistrationEnabledKey: `false`,
		CaptchaKey:             `{"provider":"","site_key":""}`,
	}
	database := &atomicUpdateDB{settings: maps.Clone(initial), failUpsertAt: 5}
	sqlDB := sql.OpenDB(atomicUpdateConnector{driver: &atomicUpdateDriver{db: database}})
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	store := New(client.New(sqlDB), nil)

	address := "new.example.com:9443"
	proxy := HTTPProxyConfig{TrustAll: true, ClientIPHeaders: []string{"X-Real-IP"}}
	localLogin := false
	retention := JobRetentionConfig{HistoryDays: 30, VersionsPerSite: 10}
	cipher := &atomicUpdateCipher{}
	prepared, err := PrepareAdminSettingsUpdate(AdminSettingsUpdate{
		AgentGatewayPublicAddress: &address,
		HTTPProxy:                 &proxy,
		LocalLoginEnabled:         &localLogin,
		JobRetention:              &retention,
		RequireTOTP:               true,
		AuthProviders: []AuthProviderConfig{{
			ID: "provider-1", Type: AuthProviderOIDC, ProviderName: "SSO", ClientSecret: "provider-secret",
		}},
		Captcha: CaptchaConfig{
			Provider: CaptchaProviderCloudflare, SiteKey: "site-key", SecretKey: "captcha-secret",
		},
	}, nil, CaptchaConfig{}, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if cipher.encryptions != 2 {
		t.Fatalf("secret encryptions = %d, want 2", cipher.encryptions)
	}
	if database.beginCount != 0 {
		t.Fatal("database transaction began before validation and encryption completed")
	}

	err = store.ApplyAdminSettingsUpdate(context.Background(), prepared, nil)
	if err == nil || !strings.Contains(err.Error(), "injected setting write failure") {
		t.Fatalf("ApplyAdminSettingsUpdate() error = %v", err)
	}
	if !maps.Equal(database.settings, initial) {
		encoded, _ := json.Marshal(database.settings)
		t.Fatalf("settings changed after rollback: %s", encoded)
	}
	if database.audits != 0 {
		t.Fatalf("committed audit entries = %d, want 0", database.audits)
	}
	if database.upsertCount != database.failUpsertAt || database.beginCount != 1 ||
		database.commitCount != 0 || database.rollbackCount != 1 {
		t.Fatalf(
			"transaction counters = upserts:%d begin:%d commit:%d rollback:%d",
			database.upsertCount, database.beginCount, database.commitCount, database.rollbackCount,
		)
	}
}

func TestApplyAdminSettingsUpdateRollsBackWhenTransactionHookFails(t *testing.T) {
	initial := map[string]string{RequireTOTPKey: `false`}
	database := &atomicUpdateDB{settings: maps.Clone(initial)}
	sqlDB := sql.OpenDB(atomicUpdateConnector{driver: &atomicUpdateDriver{db: database}})
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	store := New(client.New(sqlDB), nil)

	prepared, err := PrepareAdminSettingsUpdate(AdminSettingsUpdate{}, nil, CaptchaConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hookErr := errors.New("injected notification failure")
	err = store.ApplyAdminSettingsUpdate(context.Background(), prepared, func(*client.Client) error {
		return hookErr
	})
	if !errors.Is(err, hookErr) {
		t.Fatalf("ApplyAdminSettingsUpdate() error = %v, want %v", err, hookErr)
	}
	if !maps.Equal(database.settings, initial) || database.commitCount != 0 || database.rollbackCount != 1 {
		t.Fatalf("transaction was not rolled back: settings=%v commit=%d rollback=%d", database.settings, database.commitCount, database.rollbackCount)
	}
}
