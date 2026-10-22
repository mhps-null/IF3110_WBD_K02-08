package connections

import (
	"time"

	"github.com/example/if3110-milestone1/internal/contract"
)

// Connection represents a target database connection profile.
type Connection struct {
	ID           int64
	Name         string
	Engine       contract.Engine
	Host         string
	Port         int
	DatabaseName string
	Username     string
	PasswordEnc  string
	SSLMode      string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
