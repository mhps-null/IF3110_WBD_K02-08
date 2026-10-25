package admin

import (
	"net/http"
	"strconv"
)

func (h *Handler) AddMember(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		redirect(w, r, "/admin/groups/"+strconv.FormatInt(groupID, 10)+"?flash=error")
		return
	}

	userID, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		redirect(w, r, "/admin/groups/"+strconv.FormatInt(groupID, 10)+"?flash=error")
		return
	}

	if err := h.store.AddMember(r.Context(), groupID, userID); err != nil {
		h.logger.Error("add member failed")
		redirect(w, r, "/admin/groups/"+strconv.FormatInt(groupID, 10)+"?flash=error")
		return
	}

	redirect(w, r, "/admin/groups/"+strconv.FormatInt(groupID, 10)+"?flash=member")
}

func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}

	userID, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil || userID <= 0 {
		http.NotFound(w, r)
		return
	}

	if err := h.store.RemoveMember(r.Context(), groupID, userID); err != nil {
		h.logger.Error("remove member failed")
		redirect(w, r, "/admin/groups/"+strconv.FormatInt(groupID, 10)+"?flash=error")
		return
	}

	redirect(w, r, "/admin/groups/"+strconv.FormatInt(groupID, 10)+"?flash=removed")
}
