package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/example/if3110-milestone1/internal/contract"
)

type matrixCell struct {
	DBID int64
	DBName string
	Level int // 0 = none, 1 = view only, 2 = view + edit
}
type matrixRow struct {
	Group Group
	Cells []matrixCell
}
type matrixPageData struct {
	Rows []matrixRow
}

func (h *Handler) ShowMatrix(w http.ResponseWriter, r *http.Request) {
	groups, err := h.store.ListGroups(r.Context())
	if err != nil {
		h.logger.Error("matrix: list groups failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	dbs, err := h.store.ListDatabases(r.Context())
	if err != nil {
		h.logger.Error("matrix: list databases failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	access, err := h.store.ListGroupAccess(r.Context())
	if err != nil {
		h.logger.Error("matrix: list access failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// map (groupID, dbID)
	levels := make(map[int64]map[int64]int, len(groups))
	for _, a := range access {
		if _, ok := levels[a.GroupID]; !ok {
			levels[a.GroupID] = make(map[int64]int)
		}
		levels[a.GroupID][a.DBID] = a.Level
	}

	rows := make([]matrixRow, 0, len(groups))
	for _, g := range groups {
		row := matrixRow{Group: g}
		for _, d := range dbs {
			row.Cells = append(row.Cells, matrixCell{
				DBID: d.ID, DBName: d.Name, Level: levels[g.ID][d.ID],
			})
		}
		rows = append(rows, row)
	}

	h.render(w, r, "admin/access.html", contract.PageData{
		Flash: flashFor(r, map[string]string{
			"saved": "Matriks akses berhasil disimpan.",
			"error": "Penyimpanan gagal.",
		}),
		Data: matrixPageData{Rows: rows},
	})
}

// SetAccess nyimpen satu baris matriks (satu group, semua database)
// Form berisi group_id + field level_<dbID> utk setiap database yg ditampilkan
// Store udh idempoten dan hanya bump privilege_version kalo level bener2 berubah
func (h *Handler) SetAccess(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		redirect(w, r, "/admin/access?flash=error")
		return
	}

	groupID, err := strconv.ParseInt(r.FormValue("group_id"), 10, 64)
	if err != nil || groupID <= 0 {
		redirect(w, r, "/admin/access?flash=error")
		return
	}

	for key, values := range r.PostForm {
		if !strings.HasPrefix(key, "level_") || len(values) == 0 {
			continue
		}

		dbIDStr := strings.TrimPrefix(key, "level_")
		dbID, err := strconv.ParseInt(dbIDStr, 10, 64)
		if err != nil || dbID <= 0 {
			continue
		}

		raw := values[0]
		if raw == "" {
			if err := h.store.RemoveGroupAccess(r.Context(), groupID, dbID); err != nil {
				h.logger.Error("matrix: remove access failed")
				redirect(w, r, "/admin/access?flash=error")
				return
			}
			continue
		}

		level, err := strconv.Atoi(raw)
		if err != nil || (level != 1 && level != 2) {
			continue
		}

		if err := h.store.SetGroupAccess(r.Context(), groupID, dbID, level); err != nil {
			h.logger.Error("matrix: set access failed")
			redirect(w, r, "/admin/access?flash=error")
			return
		}
	}
	redirect(w, r, "/admin/access?flash=saved")
}
