package connections

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type cachedPool struct {
	db         *sql.DB
	engine     contract.Engine
	generation uint64
}

// PoolManager manages cached database connection pools keyed by profile ID.
type PoolManager struct {
	repo         Repository
	driverName   string
	mu           sync.RWMutex
	pools        map[int64]*cachedPool
	invalidation map[int64]uint64
	closed       bool
}

func resolveDriver(override string, engine contract.Engine) string {
	if override != "" && override != "pgx" && override != "mysql" {
		return override
	}
	switch engine {
	case contract.EngineMySQL, contract.EngineMariaDB:
		return "mysql"
	default:
		return "pgx"
	}
}

func NewPoolManager(repo Repository, driverName string) *PoolManager {
	return &PoolManager{
		repo:         repo,
		driverName:   driverName,
		pools:        make(map[int64]*cachedPool),
		invalidation: make(map[int64]uint64),
	}
}

func (p *PoolManager) Open(ctx context.Context, dbID int64) (*sql.DB, contract.Engine, error) {
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return nil, "", contract.ErrConnectionFailed
	}
	cp, exists := p.pools[dbID]
	expectedGen := p.invalidation[dbID]
	p.mu.RUnlock()

	if exists {
		return cp.db, cp.engine, nil
	}

	conn, plainPassword, err := p.repo.FindByIDWithPassword(ctx, dbID)
	if err != nil {
		return nil, "", err	
	}

	dsn, err := BuildDSN(conn, plainPassword)
	if err != nil {
		return nil, "", fmt.Errorf("build connection string: %w", err)
	}

	driver := resolveDriver(p.driverName, conn.Engine)
	targetDB, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, "", contract.ErrConnectionFailed
	}

	targetDB.SetMaxOpenConns(10)
	targetDB.SetMaxIdleConns(2)
	targetDB.SetConnMaxLifetime(15 * time.Minute)
	targetDB.SetConnMaxIdleTime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = targetDB.PingContext(pingCtx)
	cancel()
	if err != nil {
		_ = targetDB.Close()
		return nil, "", contract.ErrConnectionFailed
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		_ = targetDB.Close()
		return nil, "", contract.ErrConnectionFailed
	}

	if p.invalidation[dbID] != expectedGen {
		_ = targetDB.Close()
		return nil, "", contract.ErrConnectionFailed
	}

	if existing, alreadyCached := p.pools[dbID]; alreadyCached {
		_ = targetDB.Close()
		return existing.db, existing.engine, nil
	}

	p.pools[dbID] = &cachedPool{
		db:         targetDB,
		engine:     conn.Engine,
		generation: expectedGen,
	}

	return targetDB, conn.Engine, nil
}

func (p *PoolManager) Invalidate(dbID int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	p.invalidation[dbID]++
	if cp, exists := p.pools[dbID]; exists {
		delete(p.pools, dbID)
		return cp.db.Close()
	}
	return nil
}

func (p *PoolManager) CloseAll() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true

	var firstErr error
	for id, cp := range p.pools {
		if err := cp.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(p.pools, id)
	}
	return firstErr
}

// TestConnection verifies credentials and connectivity to a database without caching the pool.
func TestConnection(ctx context.Context, driverName string, conn *Connection, plainPassword string) error {
	driver := resolveDriver(driverName, conn.Engine)

	dsn, err := BuildDSN(conn, plainPassword)
	if err != nil {
		return fmt.Errorf("build connection string: %w", err)
	}

	tempDB, err := sql.Open(driver, dsn)
	if err != nil {
		return contract.ErrConnectionFailed
	}
	defer tempDB.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := tempDB.PingContext(pingCtx); err != nil {
		return contract.ErrConnectionFailed
	}

	return nil
}
