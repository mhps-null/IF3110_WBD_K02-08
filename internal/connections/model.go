package connections

import (
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
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
