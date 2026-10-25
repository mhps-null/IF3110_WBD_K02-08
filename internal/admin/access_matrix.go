package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type matrixCell struct {
	DBID   int64
	DBName string
	Level  int // 0 = none, 1 = view only, 2 = view + edit
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

// SetAccess nyimpen satu baris matriks (satu group, semua database).
// Form berisi group_id + field level_<dbID> utk setiap database yg ditampilkan.
// Seluruh level dan ID divalidasi ketat dan disimpan dalam satu transaksi atomik.
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

	entries := make(map[int64]int)
	for key, values := range r.PostForm {
		if key == "csrf_token" || key == "group_id" {
			continue
		}

		if !strings.HasPrefix(key, "level_") || len(values) != 1 {
			redirect(w, r, "/admin/access?flash=error")
			return
		}

		dbIDStr := strings.TrimPrefix(key, "level_")
		dbID, err := strconv.ParseInt(dbIDStr, 10, 64)
		if err != nil || dbID <= 0 {
			redirect(w, r, "/admin/access?flash=error")
			return
		}

		raw := values[0]
		var level int
		switch raw {
		case "", "0":
			level = 0
		case "1":
			level = 1
		case "2":
			level = 2
		default:
			redirect(w, r, "/admin/access?flash=error")
			return
		}

		entries[dbID] = level
	}

	if err := h.store.SetGroupAccessLevels(r.Context(), groupID, entries); err != nil {
		h.logger.Error("matrix: set access failed", "error", err)
		redirect(w, r, "/admin/access?flash=error")
		return
	}

	redirect(w, r, "/admin/access?flash=saved")
}
