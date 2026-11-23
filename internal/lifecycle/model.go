// Package lifecycle implements the Milestone 2 migration request/review/history protocol
// SQL is never executed in an HTTP handler: the worker owns target-database execution
package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalid   = errors.New("invalid migration request")
	ErrConflict  = errors.New("migration state conflict")
	ErrForbidden = errors.New("permission denied")
	ErrNotFound  = errors.New("record not found")
)

const (
	Draft         = "draft"
	PendingReview = "pending_review"
	Approved      = "approved"
	Pending       = "pending"
	Running       = "running"
	Success       = "success"
	Failed        = "failed"
	Rejected      = "rejected"
	Up            = "up"
	Rollback      = "rollback"
	Forward       = "forward"
)

type DefinitionInput struct {
	DBID               int64  `json:"db_id"`
	Title              string `json:"title"`
	SQLUp              string `json:"sql_up"`
	SQLDown            string `json:"sql_down"`
	IrreversibleReason string `json:"irreversible_reason"`
}
type Request struct {
	ID                int64  `json:"id"`
	MigrationID       int64  `json:"migration_id"`
	RevisionID        int64  `json:"revision_id"`
	DBID              int64  `json:"db_id"`
	Action            string `json:"action"`
	TargetExecutionID *int64 `json:"target_execution_id,omitempty"`
	RequestedBy       int64  `json:"requested_by"`
	Status            string `json:"status"`
}
type Approval struct {
	GroupID    int64      `json:"group_id"`
	LeaderID   *int64     `json:"leader_id"`
	ApprovedBy *int64     `json:"approved_by"`
	Decision   *string    `json:"decision"`
	DecidedBy  *int64     `json:"decided_by"`
	DecidedAt  *time.Time `json:"decided_at"`
}
type ApprovalDecision struct {
	ID         int64     `json:"id"`
	GroupID    int64     `json:"group_id"`
	LeaderID   *int64    `json:"leader_id"`
	ActorID    int64     `json:"actor_id"`
	Decision   string    `json:"decision"`
	DecidedAt  time.Time `json:"decided_at"`
	RevisionID int64     `json:"revision_id"`
}
type Execution struct {
	ID           int64   `json:"execution_id"`
	AttemptID    int64   `json:"attempt_id"`
	RequestID    int64   `json:"request_id"`
	MigrationID  int64   `json:"migration_id"`
	RevisionID   int64   `json:"revision_id"`
	ActorID      int64   `json:"user_id"`
	Action       string  `json:"action"`
	Status       string  `json:"status"`
	SQLSnapshot  string  `json:"sql_snapshot"`
	RollbackOf   *int64  `json:"rollback_of_execution_id"`
	Attempt      int     `json:"attempt"`
	ExecutedAt   string  `json:"executed_at"`
	ErrorMessage *string `json:"error_message,omitempty"`
}

func ValidateDefinition(d DefinitionInput) error {
	if d.DBID <= 0 || strings.TrimSpace(d.Title) == "" || strings.TrimSpace(d.SQLUp) == "" {
		return ErrInvalid
	}
	reversible := strings.TrimSpace(d.SQLDown) != ""
	reason := strings.TrimSpace(d.IrreversibleReason) != ""
	if reversible == reason {
		return ErrInvalid
	}
	return nil
}

func Checksum(up, down, reason string) string {
	h := sha256.Sum256([]byte(up + "\x00" + down + "\x00" + reason))
	return hex.EncodeToString(h[:])
}

// HideRollback only removes a successful rollback and its exact referenced execution
// Failed attempts and later forward executions are always retained
func HideRollback(history []Execution) []Execution {
	hiddenAttempts := map[int64]bool{}
	hiddenSuccessfulExecutions := map[int64]bool{}
	for _, e := range history {
		if e.Action == Rollback && e.Status == Success && e.RollbackOf != nil {
			hiddenAttempts[e.AttemptID] = true
			hiddenSuccessfulExecutions[*e.RollbackOf] = true
		}
	}

	out := make([]Execution, 0, len(history))
	for _, e := range history {
		if hiddenAttempts[e.AttemptID] {
			continue
		}
		if hiddenSuccessfulExecutions[e.ID] && e.Status == Success {
			continue
		}
		out = append(out, e)
	}
	return out
}
