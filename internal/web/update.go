package web

import (
	"net/http"

	"cursor-inner/internal/selfupdate"
)

// UpdateBackend 是可选的自更新接口；后端没实现时配置页不显示更新区。
type UpdateBackend interface {
	UpdateStatus() (selfupdate.Status, error)
	CheckUpdate(auto bool) (selfupdate.Status, error)
	ApplyUpdate() error
}

func registerUpdate(mux *http.ServeMux, up UpdateBackend) {
	mux.HandleFunc("GET /api/update", func(w http.ResponseWriter, r *http.Request) {
		st, err := up.UpdateStatus()
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("POST /api/update/check", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Auto bool `json:"auto"`
		}
		_ = readJSON(r, &body)
		st, err := up.CheckUpdate(body.Auto)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("POST /api/update/apply", func(w http.ResponseWriter, r *http.Request) {
		if err := up.ApplyUpdate(); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		st, _ := up.UpdateStatus()
		writeJSON(w, http.StatusAccepted, st)
	})
}
