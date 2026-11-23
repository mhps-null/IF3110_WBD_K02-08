package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/audit"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/connections"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/observability"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct {
	Store       *Store
	TargetPools *connections.PoolManager
	Audit       audit.Store
	Logger      *observability.Logger
}

func (h Handler) record(ctx context.Context, event string, actorID, dbID, requestID int64) error {
	if h.Audit == nil {
		return nil
	}
	// Never place SQL, credentials or session tokens in audit entries.
	err := h.Audit.Record(ctx, audit.Entry{Event: event, Actor: fmt.Sprintf("user:%d", actorID), Database: fmt.Sprintf("db:%d", dbID), Status: "success", Details: map[string]any{"request_id": requestID}})
	if err != nil {
		logger := slog.Default()
		if h.Logger != nil {
			logger = h.Logger.Slog()
		}
		logger.ErrorContext(ctx, "failed to persist migration audit event", "event", event, "request_id", requestID, "error", err)
	}
	return err
}

func auditFailure(w http.ResponseWriter) {
	respond(w, http.StatusInternalServerError, map[string]string{
		"error": "operation completed but audit could not be persisted; refresh state before retrying",
	})
}

func (h Handler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	if auth == nil {
		return
	}
	wrap := func(fn http.HandlerFunc) http.Handler { return auth(fn) }
	mux.Handle("POST /api/v2/databases/{db}/migrations", wrap(h.create))
	mux.Handle("GET /api/v2/databases/{db}/requests", wrap(h.requests))
	mux.Handle("POST /api/v2/databases/{db}/migrations/{migration}/actions", wrap(h.action))
	mux.Handle("PATCH /api/v2/databases/{db}/requests/{request}", wrap(h.revise))
	mux.Handle("GET /api/v2/databases/{db}/requests/{request}", wrap(h.detail))
	mux.Handle("POST /api/v2/databases/{db}/requests/{request}/submit", wrap(h.submit))
	mux.Handle("POST /api/v2/databases/{db}/requests/{request}/refresh-review", wrap(h.refreshReview))
	mux.Handle("POST /api/v2/databases/{db}/requests/{request}/validate", wrap(h.validate))
	mux.Handle("POST /api/v2/databases/{db}/requests/{request}/approve", wrap(h.approve))
	mux.Handle("POST /api/v2/databases/{db}/requests/{request}/reject", wrap(h.reject))
	mux.Handle("POST /api/v2/databases/{db}/requests/{request}/queue", wrap(h.queue))
	mux.Handle("GET /api/v2/databases/{db}/history", wrap(h.history))
	mux.Handle("GET /api/v2/review-inbox", wrap(h.inbox))
}

func number(v string) int64 {
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n <= 0 {
		return 0
	}
	return n
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func failure(w http.ResponseWriter, err error) {
	status := 500
	switch {
	case errors.Is(err, ErrInvalid):
		status = 400
	case errors.Is(err, ErrNotFound):
		status = 404
	case errors.Is(err, ErrForbidden):
		status = 403
	case errors.Is(err, ErrConflict):
		status = 409
	}
	respond(w, status, map[string]string{"error": http.StatusText(status)})
}

func decode(r *http.Request, v any) error {
	r.Body = io.NopCloser(io.LimitReader(r.Body, (1<<20)+1))
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return ErrInvalid
	}

	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func actor(r *http.Request) int64 {
	p, ok := contract.PrincipalFrom(r.Context())
	if !ok {
		return 0
	}
	return p.UserID
}

func (h Handler) require(r *http.Request, write bool) (int64, error) {
	db := number(r.PathValue("db"))
	if db == 0 {
		return 0, ErrInvalid
	}

	yes, err := h.Store.Can(r.Context(), actor(r), db, write)
	if err != nil {
		return 0, err
	}
	if !yes {
		return 0, ErrForbidden
	}

	return db, nil
}

func (h Handler) owned(r *http.Request, write bool) (Request, error) {
	db, err := h.require(r, write)
	if err != nil {
		return Request{}, err
	}

	id := number(r.PathValue("request"))
	if id == 0 {
		return Request{}, ErrInvalid
	}

	q, err := h.Store.Get(r.Context(), id)
	if err != nil {
		return q, err
	}
	if q.DBID != db {
		return Request{}, ErrNotFound
	}

	return q, nil
}

func (h Handler) create(w http.ResponseWriter, r *http.Request) {
	db, err := h.require(r, true)
	if err != nil {
		failure(w, err)
		return
	}

	var in DefinitionInput
	if err = decode(r, &in); err != nil {
		failure(w, err)
		return
	}

	in.DBID = db
	q, err := h.Store.Create(r.Context(), actor(r), in)
	if err != nil {
		failure(w, err)
		return
	}

	if err = h.record(r.Context(), "migration.create", actor(r), db, q.ID); err != nil {
		auditFailure(w)
		return
	}
	respond(w, 201, q)
}
func (h Handler) action(w http.ResponseWriter, r *http.Request) {
	db, err := h.require(r, true)
	if err != nil {
		failure(w, err)
		return
	}

	var in struct {
		Action string `json:"action"`
	}

	if err = decode(r, &in); err != nil {
		failure(w, err)
		return
	}

	mid := number(r.PathValue("migration"))
	if mid == 0 {
		failure(w, ErrInvalid)
		return
	}

	q, err := h.Store.CreateAction(r.Context(), actor(r), db, mid, in.Action)
	if err != nil {
		failure(w, err)
		return
	}

	if err = h.record(r.Context(), "migration.action.create", actor(r), db, q.ID); err != nil {
		auditFailure(w)
		return
	}
	respond(w, 201, q)
}

func (h Handler) revise(w http.ResponseWriter, r *http.Request) {
	q, err := h.owned(r, true)
	if err != nil {
		failure(w, err)
		return
	}

	var in DefinitionInput
	if err = decode(r, &in); err != nil {
		failure(w, err)
		return
	}

	in.DBID = q.DBID
	out, err := h.Store.Revise(r.Context(), q.ID, actor(r), in)
	if err != nil {
		failure(w, err)
		return
	}

	if err = h.record(r.Context(), "migration.revise", actor(r), q.DBID, q.ID); err != nil {
		auditFailure(w)
		return
	}
	respond(w, 200, out)
}

func (h Handler) detail(w http.ResponseWriter, r *http.Request) {
	q, err := h.owned(r, false)
	if err != nil {
		failure(w, err)
		return
	}

	a, err := h.Store.Approvals(r.Context(), q.ID)
	if err != nil {
		failure(w, err)
		return
	}
	approvalHistory, err := h.Store.ApprovalHistory(r.Context(), q.ID)
	if err != nil {
		failure(w, err)
		return
	}

	definition, err := h.Store.RevisionDetails(r.Context(), q.ID)
	if err != nil {
		failure(w, err)
		return
	}

	respond(w, 200, map[string]any{"request": q, "approvals": a, "approval_history": approvalHistory, "definition": definition})
}

func (h Handler) submit(w http.ResponseWriter, r *http.Request) {
	q, err := h.owned(r, true)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.validateRequest(r.Context(), q.ID); err != nil {
		failure(w, err)
		return
	}

	out, err := h.Store.Submit(r.Context(), q.ID, actor(r))
	if err != nil {
		failure(w, err)
		return
	}

	if err = h.record(r.Context(), "migration.submit", actor(r), q.DBID, q.ID); err != nil {
		auditFailure(w)
		return
	}
	respond(w, 200, out)
}

func (h Handler) refreshReview(w http.ResponseWriter, r *http.Request) {
	q, err := h.owned(r, true)
	if err != nil {
		failure(w, err)
		return
	}
	out, err := h.Store.RefreshReview(r.Context(), q.ID, actor(r))
	if err != nil {
		failure(w, err)
		return
	}
	if err = h.record(r.Context(), "migration.review.refresh", actor(r), q.DBID, q.ID); err != nil {
		auditFailure(w)
		return
	}
	respond(w, http.StatusOK, out)
}

func (h Handler) approval(w http.ResponseWriter, r *http.Request, approve bool) {
	q, err := h.owned(r, false)
	if err != nil {
		failure(w, err)
		return
	}

	out, err := h.Store.Approve(r.Context(), q.ID, actor(r), approve)
	if err != nil {
		failure(w, err)
		return
	}

	event := "migration.approve"
	if !approve {
		event = "migration.reject"
	}

	if err = h.record(r.Context(), event, actor(r), q.DBID, q.ID); err != nil {
		auditFailure(w)
		return
	}
	respond(w, 200, out)
}

func (h Handler) approve(w http.ResponseWriter, r *http.Request) { h.approval(w, r, true) }

func (h Handler) reject(w http.ResponseWriter, r *http.Request) { h.approval(w, r, false) }

func (h Handler) queue(w http.ResponseWriter, r *http.Request) {
	q, err := h.owned(r, true)
	if err != nil {
		failure(w, err)
		return
	}

	id, err := h.Store.Queue(r.Context(), q.ID, actor(r))
	if err != nil {
		failure(w, err)
		return
	}

	if err = h.record(r.Context(), "migration.enqueue", actor(r), q.DBID, q.ID); err != nil {
		auditFailure(w)
		return
	}
	respond(w, 202, map[string]any{"job_id": id, "request_id": q.ID, "status": "pending"})
}

func (h Handler) history(w http.ResponseWriter, r *http.Request) {
	db, err := h.require(r, false)
	if err != nil {
		failure(w, err)
		return
	}

	history, err := h.Store.ListHistory(r.Context(), db, strings.EqualFold(r.URL.Query().Get("hide_rollback"), "true"))
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": history})
}

func (h Handler) inbox(w http.ResponseWriter, r *http.Request) {
	if actor(r) == 0 {
		failure(w, ErrForbidden)
		return
	}

	list, err := h.Store.Inbox(r.Context(), actor(r))
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": list})
}

func (h Handler) requests(w http.ResponseWriter, r *http.Request) {
	db, err := h.require(r, false)
	if err != nil {
		failure(w, err)
		return
	}

	items, err := h.Store.ListRequests(r.Context(), db)
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}

func (h Handler) validate(w http.ResponseWriter, r *http.Request) {
	q, err := h.owned(r, true)
	if err != nil {
		failure(w, err)
		return
	}
	if err = h.validateRequest(r.Context(), q.ID); err != nil {
		failure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"valid": true, "request_id": q.ID})
}
