package jobs

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"
)

// Lock key namespace "O4" in the high bits keeps the job-engine locks disjoint
// from the lifecycle package's short transaction-level locks (db_id+2300000000).
const lockNamespace int64 = 0x4f34 << 48

// targetLockKey is the fixed key taken inside each TARGET database. Because it is
// taken on the target server itself, it fences a stale worker whose metadata
// connection (and therefore metadata lock) was lost while its target session is
// still alive: a new worker cannot start on that database until the stale target
// session ends, which also aborts any transaction it still had open.
const targetLockKey = lockNamespace | 0xffffffffffff

// ErrLockBusy means another worker currently owns the database lock.
var ErrLockBusy = errors.New("database migration lock is held by another worker")

// MetadataLockKey returns the cross-worker lock key for one registered database.
func MetadataLockKey(dbID int64) int64 { return lockNamespace | (dbID & 0xffffffffffff) }

// DBLock is a two-level, session-scoped PostgreSQL advisory lock:
//
//  1. metadata DB, key per database ID: shared by every worker instance, so at
//     most one worker runs a migration for a database at a time (all batches
//     included, the lock is held between batch commits);
//  2. target DB, fixed key: fencing on the data itself.
//
// Both are session locks on dedicated connections. If a worker process dies, its
// connections close and PostgreSQL releases both locks automatically (lock
// recovery needs no manual step). pg_advisory_unlock only succeeds from the owning
// session, so one worker can never release a lock held by another.
type DBLock struct {
	dbID   int64
	meta   *sql.Conn
	target *sql.Conn
}

// AcquireDBLock tries to take both locks, polling until wait elapses.
func AcquireDBLock(ctx context.Context, metaDB, targetDB *sql.DB, dbID int64, wait time.Duration) (*DBLock, error) {
	deadline := time.Now().Add(wait)
	for {
		l, err := tryAcquire(ctx, metaDB, targetDB, dbID)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, ErrLockBusy) || time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func tryAcquire(ctx context.Context, metaDB, targetDB *sql.DB, dbID int64) (*DBLock, error) {
	meta, err := metaDB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	ok, err := tryLock(ctx, meta, MetadataLockKey(dbID))
	if err != nil || !ok {
		_ = meta.Close()
		if err == nil {
			err = ErrLockBusy
		}
		return nil, err
	}
	target, err := targetDB.Conn(ctx)
	if err != nil {
		unlockAndClose(meta, MetadataLockKey(dbID))
		return nil, err
	}
	ok, err = tryLock(ctx, target, targetLockKey)
	if err != nil || !ok {
		_ = target.Close()
		unlockAndClose(meta, MetadataLockKey(dbID))
		if err == nil {
			err = ErrLockBusy
		}
		return nil, err
	}
	return &DBLock{dbID: dbID, meta: meta, target: target}, nil
}

func tryLock(ctx context.Context, conn *sql.Conn, key int64) (bool, error) {
	var ok bool
	err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&ok)
	return ok, err
}

func unlockAndClose(conn *sql.Conn, key int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var released bool
	_ = conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, key).Scan(&released)
	// Closing returns the connection to the pool; if unlock failed the session is
	// discarded instead so the server releases the lock when it disconnects.
	if !released {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	_ = conn.Close()
}

// Alive verifies both lock sessions are still connected. A worker must stop
// before committing further work when this fails, because the lock may be gone.
func (l *DBLock) Alive(ctx context.Context) error {
	if l == nil {
		return ErrLockBusy
	}
	if err := l.meta.PingContext(ctx); err != nil {
		return err
	}
	return l.target.PingContext(ctx)
}

// Release unlocks and returns both sessions. Safe to call more than once.
func (l *DBLock) Release() {
	if l == nil {
		return
	}
	if l.target != nil {
		unlockAndClose(l.target, targetLockKey)
		l.target = nil
	}
	if l.meta != nil {
		unlockAndClose(l.meta, MetadataLockKey(l.dbID))
		l.meta = nil
	}
}
