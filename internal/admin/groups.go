package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/example/if3110-milestone1/internal/contract"
)

type groupsPageData struct {
	Groups []Group
}

type groupDetailPageData struct {
	Group Group
	Members []User
	Available []User // user yg blm jadi anggota
}

func (h *Handler) ListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.store.ListGroups(r.Context())
	if err != nil {
		h.logger.Error("list groups failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	h.render(w, r, "admin/groups.html", contract.PageData{
		Flash: flashFor(r, map[string]string{
			"created": "Group berhasil dibuat.",
			"deleted": "Group berhasil dihapus.",
			"member": "Anggota berhasil ditambahkan.",
			"removed": "Anggota berhasil dihapus.",
			"error": "Operasi gagal. Silakan coba lagi.",
		}),
		Data: groupsPageData{Groups: groups},
	})
}

func (h *Handler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		redirect(w, r, "/admin/groups?flash=error")
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	if name == "" || len(name) > 100 {
		redirect(w, r, "/admin/groups?flash=error")
		return
	}

	if _, err := h.store.CreateGroup(r.Context(), name, description); err != nil {
		if !errors.Is(err, ErrDuplicateGroupName) {
			h.logger.Error("create group failed")
		}
		redirect(w, r, "/admin/groups?flash=error")
		return
	}

	redirect(w, r, "/admin/groups?flash=created")
}

func (h *Handler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}

	if err := h.store.DeleteGroup(r.Context(), id); err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			http.NotFound(w, r)
			return
		}

		h.logger.Error("delete group failed")
		redirect(w, r, "/admin/groups?flash=error")
		return
	}

	redirect(w, r, "/admin/groups?flash=deleted")
}

func (h *Handler) ShowGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}

	g, err := h.store.GetGroup(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			http.NotFound(w, r)
			return
		}

		h.logger.Error("get group failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	members, err := h.store.ListGroupMembers(r.Context(), id)
	if err != nil {
		h.logger.Error("list members failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	all, err := h.store.ListUsers(r.Context())
	if err != nil {
		h.logger.Error("list users failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	isMember := make(map[int64]bool, len(members))
	for _, m := range members {
		isMember[m.ID] = true
	}

	var available []User
	for _, u := range all {
		if !isMember[u.ID] {
			available = append(available, u)
		}
	}

	h.render(w, r, "admin/group_detail.html", contract.PageData{
		Flash: flashFor(r, map[string]string{
			"member": "Anggota berhasil ditambahkan.",
			"removed": "Anggota berhasil dihapus.",
			"error": "Operasi gagal.",
		}),
		Data: groupDetailPageData{Group: g, Members: members, Available: available},
	})
}

// flashFor memetakan kode flash statis dari query param ke pesan.
// Kode harus berasal dari daftar tertutup agar tidak ada open redirect / echo.
func flashFor(r *http.Request, messages map[string]string) string {
	code := r.URL.Query().Get("flash")
	if msg, ok := messages[code]; ok {
		return msg
	}
	return ""
}

func redirect(w http.ResponseWriter, r *http.Request, location string) {
	http.Redirect(w, r, location, http.StatusSeeOther)
}
