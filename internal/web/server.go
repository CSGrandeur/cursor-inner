package web

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"cursor-inner/assets"
	"cursor-inner/internal/config"
)

//go:embed page.html
var page embed.FS

func Handler(backend Backend) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		raw, _ := page.ReadFile("page.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /icon.svg", func(w http.ResponseWriter, r *http.Request) {
		raw := assets.IconSVG
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		view, err := backend.State()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})
	mux.HandleFunc("PUT /api/takeover", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := backend.SetTakeover(body.Enabled); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeState(w, backend)
	})
	mux.HandleFunc("PUT /api/proxy", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool   `json:"enabled"`
			Address string `json:"address"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := backend.SetProxy(body.Enabled, body.Address); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeState(w, backend)
	})
	mux.HandleFunc("PUT /api/autostart", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := backend.SetAutostart(body.Enabled); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeState(w, backend)
	})
	mux.HandleFunc("POST /api/models", func(w http.ResponseWriter, r *http.Request) {
		var model config.Model
		if err := readJSON(r, &model); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := backend.AddModel(model); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeState(w, backend)
	})
	mux.HandleFunc("POST /api/models/test", func(w http.ResponseWriter, r *http.Request) {
		var model config.Model
		if err := readJSON(r, &model); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, backend.TestDraft(model))
	})
	mux.HandleFunc("DELETE /api/models/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := backend.DeleteModel(r.PathValue("id")); err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeState(w, backend)
	})
	mux.HandleFunc("PUT /api/models/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			UseProxy bool `json:"use_proxy"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := backend.SetModelProxy(r.PathValue("id"), body.UseProxy); err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeState(w, backend)
	})
	mux.HandleFunc("POST /api/models/{id}/test", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, backend.TestSaved(r.PathValue("id")))
	})
	return mux
}

func writeState(w http.ResponseWriter, backend Backend) {
	view, err := backend.State()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("请求内容无法解析")
	}
	return nil
}
