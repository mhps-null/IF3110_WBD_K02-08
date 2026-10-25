package admin

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
)

type matrixPageData struct {
	Databases        []Database
	SelectedDatabase int64
	SelectedDBName   string
	Groups           []Group
	SelectedGroupID  int64
	SelectedGroup    string
	SelectedLevel    int
	GroupSelected    bool
	AccessEntries    []accessMatrixEntry
}

type accessMatrixEntry struct {
	DBID         int64
	GroupID      int64
	DatabaseName string
	GroupName    string
	Level        int
}

func (h *Handler) ShowMatrix(w http.ResponseWriter, r *http.Request) {
	dbs, err := h.store.ListDatabases(r.Context())
	if err != nil {
		h.logger.Error("matrix: list databases failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	data := matrixPageData{Databases: dbs}
	groups, err := h.store.ListGroups(r.Context())
	if err != nil {
		h.logger.Error("matrix: list groups failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	access, err := h.store.ListGroupAccess(r.Context())
	if err != nil {
		h.logger.Error("matrix: list access failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	groupNames := make(map[int64]string, len(groups))
	for _, group := range groups {
		groupNames[group.ID] = group.Name
	}

	databaseNames := make(map[int64]string, len(dbs))
	for _, db := range dbs {
		databaseNames[db.ID] = db.Name
	}

	for _, entry := range access {
		if entry.Level == 0 {
			continue
		}

		groupName, groupExists := groupNames[entry.GroupID]
		databaseName, databaseExists := databaseNames[entry.DBID]
		if groupExists && databaseExists {
			data.AccessEntries = append(data.AccessEntries, accessMatrixEntry{
				DBID:         entry.DBID,
				GroupID:      entry.GroupID,
				DatabaseName: databaseName,
				GroupName:    groupName,
				Level:        entry.Level,
			})
		}
	}

	sort.Slice(data.AccessEntries, func(i, j int) bool {
		if data.AccessEntries[i].DatabaseName != data.AccessEntries[j].DatabaseName {
			return data.AccessEntries[i].DatabaseName < data.AccessEntries[j].DatabaseName
		}
		return data.AccessEntries[i].GroupName < data.AccessEntries[j].GroupName
	})

	data.Groups = groups
	selectedDBID, parseErr := strconv.ParseInt(r.URL.Query().Get("db_id"), 10, 64)
	if parseErr == nil && selectedDBID > 0 {
		for _, db := range dbs {
			if db.ID == selectedDBID {
				data.SelectedDatabase = db.ID
				data.SelectedDBName = db.Name
				break
			}
		}
	}

	selectedGroupID, err := strconv.ParseInt(r.URL.Query().Get("group_id"), 10, 64)
	if err == nil && selectedGroupID > 0 {
		for _, group := range groups {
			if group.ID == selectedGroupID {
				data.SelectedGroupID = group.ID
				data.SelectedGroup = group.Name
				data.GroupSelected = true
				break
			}
		}
	}

	if data.SelectedDatabase != 0 && data.GroupSelected {
		for _, item := range access {
			if item.GroupID == data.SelectedGroupID && item.DBID == data.SelectedDatabase {
				data.SelectedLevel = item.Level
				break
			}
		}
	}

	h.render(w, r, "admin/access.html", contract.PageData{
		Flash: flashFor(r, map[string]string{
			"saved": "Matriks akses berhasil disimpan.",
			"error": "Penyimpanan gagal.",
		}),
		Data: data,
	})
}

// simpan level akses yang dipilih untuk satu group dan database
func (h *Handler) SetAccess(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		redirect(w, r, "/admin/access?flash=error")
		return
	}

	dbID, err := strconv.ParseInt(r.FormValue("db_id"), 10, 64)
	if err != nil || dbID <= 0 {
		redirect(w, r, "/admin/access?flash=error")
		return
	}

	groupID, err := strconv.ParseInt(r.FormValue("group_id"), 10, 64)
	if err != nil || groupID <= 0 {
		redirect(w, r, "/admin/access?flash=error")
		return
	}

	if len(r.PostForm) != 4 || len(r.PostForm["csrf_token"]) != 1 ||
		len(r.PostForm["db_id"]) != 1 || len(r.PostForm["group_id"]) != 1 ||
		len(r.PostForm["level"]) != 1 || r.PostForm["csrf_token"][0] == "" {
		redirect(w, r, "/admin/access?flash=error")
		return
	}

	level, err := strconv.Atoi(r.FormValue("level"))
	if err != nil || level < 0 || level > 2 {
		redirect(w, r, "/admin/access?flash=error")
		return
	}

	if err := h.store.SetGroupAccessLevels(r.Context(), groupID, map[int64]int{dbID: level}); err != nil {
		h.logger.Error("matrix: set access failed", "error", err)
		redirect(w, r, "/admin/access?flash=error")
		return
	}

	redirect(w, r, "/admin/access?db_id="+strconv.FormatInt(dbID, 10)+
		"&group_id="+strconv.FormatInt(groupID, 10)+"&flash=saved")
}
