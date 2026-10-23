package connections

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/example/if3110-milestone1/internal/contract"
)

type poolDriver struct{}
type poolConn struct{ fail bool }

func (poolDriver) Open(dsn string) (driver.Conn, error) {
	return &poolConn{fail: strings.Contains(dsn, "fail_ping")}, nil
}
func (*poolConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not implemented") }
func (*poolConn) Begin() (driver.Tx, error)           { return nil, errors.New("not implemented") }
func (*poolConn) Close() error                        { return nil }
func (c *poolConn) Ping(context.Context) error {
	if c.fail {
		return errors.New("connection failed: postgres://user:secret@host/db")
	}
	return nil
}
func init() { sql.Register("pool_test_driver", poolDriver{}) }

func poolRepository(t *testing.T) (*MemoryRepository, *Connection) {
	t.Helper()
	repo, err := NewMemoryRepository(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	c := &Connection{Engine: contract.EnginePostgres, Host: "localhost", Port: 5432, DatabaseName: "app", Username: "user"}
	if err := repo.Create(context.Background(), c, "secret"); err != nil {
		t.Fatal(err)
	}
	return repo, c
}

func TestPoolManager(t *testing.T) {
	repo, c := poolRepository(t)
	pm := NewPoolManager(repo, "pool_test_driver")
	t.Cleanup(func() { _ = pm.CloseAll() })
	ctx := context.Background()
	first, engine, err := pm.Open(ctx, c.ID)
	if err != nil || engine != contract.EnginePostgres {
		t.Fatalf("open: %v", err)
	}
	cached, _, err := pm.Open(ctx, c.ID)
	if err != nil || cached != first {
		t.Fatal("pool was not cached")
	}
	if err := pm.Invalidate(c.ID); err != nil {
		t.Fatal(err)
	}
	if err := first.PingContext(ctx); err == nil {
		t.Fatal("invalidated pool still open")
	}
	next, _, err := pm.Open(ctx, c.ID)
	if err != nil || next == first {
		t.Fatal("pool was not replaced")
	}
	if err := pm.CloseAll(); err != nil {
		t.Fatal(err)
	}
	if err := next.PingContext(ctx); err == nil {
		t.Fatal("pool still open after shutdown")
	}
	if _, _, err := pm.Open(ctx, c.ID); !errors.Is(err, contract.ErrConnectionFailed) {
		t.Fatalf("open after shutdown: %v", err)
	}
}

func TestTestConnection(t *testing.T) {
	for _, host := range []string{"localhost", "fail_ping"} {
		t.Run(host, func(t *testing.T) {
			repo, c := poolRepository(t)
			c.Host = host
			if err := repo.Update(context.Background(), c, nil); err != nil {
				t.Fatal(err)
			}
			err := TestConnection(context.Background(), "pool_test_driver", c, "secret")
			if host == "localhost" && err != nil {
				t.Fatal(err)
			}
			if host == "fail_ping" {
				if !errors.Is(err, contract.ErrConnectionFailed) || strings.Contains(err.Error(), "secret") {
					t.Fatalf("unsafe error: %v", err)
				}
				pm := NewPoolManager(repo, "pool_test_driver")
				defer pm.CloseAll()
				if _, _, err := pm.Open(context.Background(), c.ID); !errors.Is(err, contract.ErrConnectionFailed) {
					t.Fatalf("open: %v", err)
				}
				if len(pm.pools) != 0 {
					t.Fatal("failed connection was cached")
				}
			}
		})
	}
}
