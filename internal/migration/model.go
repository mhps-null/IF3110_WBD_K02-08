package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const (
	StatusDraft   = "draft"
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

type Migration struct {
	ID             int64      `json:"id"`
	DBID           int64      `json:"db_id"`
	Version        *int64     `json:"version"`
	Title          string     `json:"title"`
	SQLText        string     `json:"sql_text"`
	Status         string     `json:"status"`
	CreatedBy      int64      `json:"created_by"`
	CreatedByName  string     `json:"created_by_name,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	ExecutedBy     *int64     `json:"executed_by,omitempty"`
	ExecutedByName *string    `json:"executed_by_name,omitempty"`
	ExecutedAt     *time.Time `json:"executed_at,omitempty"`
	ErrorMessage   *string    `json:"error_message,omitempty"`
	Checksum       *string    `json:"checksum,omitempty"`
}

func (m *Migration) IsEditable() bool {
	return m.Status == StatusDraft
}

func (m *Migration) Diagnostic() *ErrorDiagnostic {
	if m == nil || m.ErrorMessage == nil || *m.ErrorMessage == "" {
		return nil
	}
	return DiagnoseError(nil, m.ErrorMessage, "")
}

func (m *Migration) Validate() error {
	if strings.TrimSpace(m.Title) == "" {
		return errors.New("judul migration tidak boleh kosong")
	}
	if strings.TrimSpace(m.SQLText) == "" {
		return errors.New("query SQL migration tidak boleh kosong")
	}
	if m.Status != StatusDraft && m.Status != StatusSuccess && m.Status != StatusFailed {
		return errors.New("status migration tidak valid")
	}
	return nil
}

func ComputeChecksum(sqlText string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sqlText)))
	return hex.EncodeToString(sum[:])
}
