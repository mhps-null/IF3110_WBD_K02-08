package connections

import (
	"time"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

// Connection represents a target database connection profile.
// PasswordEnc is intentionally excluded from JSON serialization.
type Connection struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	Engine       contract.Engine `json:"engine"`
	Host         string          `json:"host"`
	Port         int             `json:"port"`
	DatabaseName string          `json:"database_name"`
	Username     string          `json:"username"`
	PasswordEnc  string          `json:"-"`
	SSLMode      string          `json:"ssl_mode"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}
