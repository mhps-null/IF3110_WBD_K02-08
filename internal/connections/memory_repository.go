package connections

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

// MemoryRepository is an in-memory implementation of Repository for unit tests and local development.
type MemoryRepository struct {
	mu             sync.RWMutex
	connections    map[int64]Connection
	encryptionKey  []byte
	nextID         int64
	migrationCount map[int64]int
}

func NewMemoryRepository(encryptionKey []byte) (*MemoryRepository, error) {
	if len(encryptionKey) != 32 {
		return nil, ErrInvalidKey
	}
	return &MemoryRepository{
		connections:    make(map[int64]Connection),
		encryptionKey:  encryptionKey,
		nextID:         1,
		migrationCount: make(map[int64]int),
	}, nil
}

func (m *MemoryRepository) SetMigrationCount(dbID int64, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.migrationCount[dbID] = count
}

func (m *MemoryRepository) Create(ctx context.Context, conn *Connection, plainPassword string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	enc, err := Encrypt(plainPassword, m.encryptionKey)
	if err != nil {
		return err
	}

	conn.ID = m.nextID
	m.nextID++
	conn.PasswordEnc = enc
	now := time.Now().UTC()
	conn.CreatedAt = now
	conn.UpdatedAt = now

	m.connections[conn.ID] = *conn
	return nil
}

func (m *MemoryRepository) FindByID(ctx context.Context, id int64) (*Connection, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	conn, ok := m.connections[id]
	if !ok {
		return nil, contract.ErrNotFound
	}
	cp := conn
	return &cp, nil
}

func (m *MemoryRepository) FindByIDWithPassword(ctx context.Context, id int64) (*Connection, string, error) {
	conn, err := m.FindByID(ctx, id)
	if err != nil {
		return nil, "", err
	}

	plain, err := Decrypt(conn.PasswordEnc, m.encryptionKey)
	if err != nil {
		return nil, "", err
	}

	return conn, plain, nil
}

func (m *MemoryRepository) List(ctx context.Context) ([]Connection, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Connection, 0, len(m.connections))
	for _, c := range m.connections {
		result = append(result, c)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})

	return result, nil
}

func (m *MemoryRepository) Update(ctx context.Context, conn *Connection, newPlainPassword *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.connections[conn.ID]
	if !ok {
		return contract.ErrNotFound
	}

	var encPassword string
	if newPlainPassword != nil && *newPlainPassword != "" {
		enc, err := Encrypt(*newPlainPassword, m.encryptionKey)
		if err != nil {
			return err
		}
		encPassword = enc
	} else {
		encPassword = existing.PasswordEnc
	}

	conn.PasswordEnc = encPassword
	conn.CreatedAt = existing.CreatedAt
	conn.UpdatedAt = time.Now().UTC()

	m.connections[conn.ID] = *conn
	return nil
}

func (m *MemoryRepository) Delete(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.connections[id]; !ok {
		return contract.ErrNotFound
	}

	if count, ok := m.migrationCount[id]; ok && count > 0 {
		return ErrHasMigrationHistory
	}

	delete(m.connections, id)
	return nil
}
