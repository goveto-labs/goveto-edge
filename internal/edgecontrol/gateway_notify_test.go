package edgecontrol

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"

	"goveto-edge/internal/storage/gen/client"
)

type notifyTestDB struct {
	mu        sync.Mutex
	committed int
	executed  int
}

type notifyTestConnector struct{ db *notifyTestDB }

func (c notifyTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &notifyTestConn{db: c.db}, nil
}

func (c notifyTestConnector) Driver() driver.Driver { return notifyTestDriver{db: c.db} }

type notifyTestDriver struct{ db *notifyTestDB }

func (d notifyTestDriver) Open(string) (driver.Conn, error) { return &notifyTestConn{db: d.db}, nil }

type notifyTestConn struct {
	db *notifyTestDB
	tx *notifyTestTx
}

func (*notifyTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are unsupported")
}

func (*notifyTestConn) Close() error { return nil }

func (c *notifyTestConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *notifyTestConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.tx != nil {
		return nil, errors.New("transaction already active")
	}
	c.tx = &notifyTestTx{conn: c}
	return c.tx, nil
}

func (c *notifyTestConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if c.tx == nil {
		return nil, errors.New("notification executed outside transaction")
	}
	if !strings.Contains(query, "pg_notify") {
		return nil, errors.New("unexpected query: " + query)
	}
	c.db.mu.Lock()
	c.db.executed++
	c.db.mu.Unlock()
	c.tx.pending++
	return driver.RowsAffected(1), nil
}

type notifyTestTx struct {
	conn    *notifyTestConn
	pending int
}

func (tx *notifyTestTx) Commit() error {
	tx.conn.db.mu.Lock()
	tx.conn.db.committed += tx.pending
	tx.conn.db.mu.Unlock()
	tx.conn.tx = nil
	return nil
}

func (tx *notifyTestTx) Rollback() error {
	tx.conn.tx = nil
	return nil
}

func TestNotifyAuthorityUpdateTxFollowsTransactionOutcome(t *testing.T) {
	for _, test := range []struct {
		name          string
		rollback      bool
		wantCommitted int
	}{
		{name: "commit", wantCommitted: 1},
		{name: "rollback", rollback: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &notifyTestDB{}
			sqlDB := sql.OpenDB(notifyTestConnector{db: state})
			t.Cleanup(func() { _ = sqlDB.Close() })
			db := client.New(sqlDB)
			gateway := &Gateway{instanceID: "instance-1"}
			rollbackErr := errors.New("rollback")

			err := db.Tx(context.Background(), func(tx *client.Client) error {
				if err := gateway.NotifyAuthorityUpdateTx(context.Background(), tx, "new.example.com:9443"); err != nil {
					return err
				}
				if test.rollback {
					return rollbackErr
				}
				return nil
			})
			if test.rollback && !errors.Is(err, rollbackErr) {
				t.Fatalf("transaction error = %v, want %v", err, rollbackErr)
			}
			if !test.rollback && err != nil {
				t.Fatal(err)
			}
			if state.executed != 1 || state.committed != test.wantCommitted {
				t.Fatalf("notifications executed=%d committed=%d, want 1/%d", state.executed, state.committed, test.wantCommitted)
			}
		})
	}
}
