package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/audit"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
)

// Handler exposes the job API under /api/v2. All routes require login (auth
// middleware) and mutations are protected by the global CSRF middleware.
// Authorization is enforced in Service against group database access.
type Handler struct {
	Service *Service
	Audit   audit.Store
	Logger  *observability.Logger
}

// Register mounts the routes. auth must be the session middleware.
func (h Handler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	if auth == nil || h.Service == nil {
		return
	}
	wrap := func(fn http.HandlerFunc) http.Handler { return auth(fn) }
	mux.Handle("POST /api/v2/databases/{db}/requests/{request}/execute", wrap(h.execute))
	mux.Handle("GET /api/v2/jobs", wrap(h.list))
	mux.Handle("GET /api/v2/jobs/{job}", wrap(h.detail))
	mux.Handle("POST /api/v2/jobs/{job}/resume", wrap(h.control("resume")))
	mux.Handle("POST /api/v2/jobs/{job}/cancel", wrap(h.control("cancel")))
	mux.Handle("POST /api/v2/jobs/{job}/compensate", wrap(h.control("compensate")))
}

func pathID(r *http.Request, name string) int64 {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func principal(r *http.Request) int64 {
	if p, ok := contract.PrincipalFrom(r.Context()); ok {
		return p.UserID
	}
	return 0
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr maps errors to HTTP status. Messages for 400/409 are authored by this
// package (never raw driver errors); 5xx return a generic message.
func (h Handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	msg := "internal error"
	switch {
	case errors.Is(err, ErrInvalid):
		status, msg = http.StatusBadRequest, userMessage(err, "invalid request")
	case errors.Is(err, ErrForbidden):
		status, msg = http.StatusForbidden, "permission denied"
	case errors.Is(err, ErrNotFound):
		status, msg = http.StatusNotFound, "not found"
	case errors.Is(err, ErrConflict):
		status, msg = http.StatusConflict, userMessage(err, "conflict")
	default:
		logger := slog.Default()
		if h.Logger != nil {
			logger = h.Logger.Slog()
		}
		logger.ErrorContext(r.Context(), "job api error", "path", observability.SanitizeLogString(r.URL.Path), "error", observability.RedactString(err.Error()))
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func userMessage(err error, fallback string) string {
	s := err.Error()
	for _, sentinel := range []error{ErrInvalid, ErrConflict} {
		s = strings.TrimSuffix(s, ": "+sentinel.Error())
	}
	s = strings.TrimSpace(s)
	if s == "" || s == ErrInvalid.Error() || s == ErrConflict.Error() {
		return fallback
	}
	return s
}

func decodeOptional(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty body: defaults
		}
		return fmt.Errorf("malformed JSON body: %w", ErrInvalid)
	}
	if dec.More() {
		return fmt.Errorf("unexpected trailing JSON: %w", ErrInvalid)
	}
	return nil
}

func (h Handler) record(ctx context.Context, event string, actor int64, j Job, details map[string]any) {
	if h.Audit == nil {
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	details["job_id"] = j.ID
	details["request_id"] = j.RequestID
	details["mode"] = j.Mode
	err := h.Audit.Record(ctx, audit.Entry{Event: event, Actor: fmt.Sprintf("user:%d", actor),
		Database: fmt.Sprintf("db:%d", j.DBID), Status: "success", Details: details})
	if err != nil && h.Logger != nil {
		h.Logger.Slog().ErrorContext(ctx, "failed to persist job audit event", "event", event, "job_id", j.ID)
	}
}

func (h Handler) execute(w http.ResponseWriter, r *http.Request) {
	db, req := pathID(r, "db"), pathID(r, "request")
	if db == 0 || req == 0 {
		h.writeErr(w, r, ErrInvalid)
		return
	}
	var in ExecuteOptions
	if err := decodeOptional(w, r, &in); err != nil {
		h.writeErr(w, r, err)
		return
	}
	actor := principal(r)
	j, err := h.Service.Execute(r.Context(), actor, db, req, in)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	h.record(r.Context(), "migration.job.enqueue", actor, j, map[string]any{"action": j.Action})
	if h.Logger != nil {
		h.Logger.LogActivity(r.Context(), "job.enqueued", "", fmt.Sprintf("db:%d", j.DBID), "queued",
			slog.Int64("job_id", j.ID), slog.Int64("request_id", j.RequestID), slog.String("mode", j.Mode))
	}
	// 202: the job is only queued; success is reported later by the worker.
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": j.ID, "request_id": j.RequestID, "status": j.Status, "job": j})
}

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := ListFilter{Status: strings.TrimSpace(q.Get("status"))}
	if v := q.Get("db_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			h.writeErr(w, r, ErrInvalid)
			return
		}
		f.DBID = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			h.writeErr(w, r, ErrInvalid)
			return
		}
		f.Limit = n
	}
	items, err := h.Service.List(r.Context(), principal(r), f)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h Handler) detail(w http.ResponseWriter, r *http.Request) {
	d, err := h.Service.Get(r.Context(), principal(r), pathID(r, "job"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h Handler) control(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, actor := pathID(r, "job"), principal(r)
		var (
			j   Job
			err error
		)
		switch action {
		case "resume":
			j, err = h.Service.Resume(r.Context(), actor, id)
		case "cancel":
			j, err = h.Service.Cancel(r.Context(), actor, id)
		default:
			j, err = h.Service.Compensate(r.Context(), actor, id)
		}
		if err != nil {
			h.writeErr(w, r, err)
			return
		}
		h.record(r.Context(), "migration.job."+action, actor, j, nil)
		writeJSON(w, http.StatusAccepted, map[string]any{"job": j})
	}
}
