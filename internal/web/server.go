package web

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"cursor-inner/internal/i18n"

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
	mux.HandleFunc("PUT /api/image", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
			Model   string `json:"model"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := backend.SetImage(body.BaseURL, body.APIKey, body.Model); err != nil {
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
			DisplayName     *string `json:"display_name"`
			Type            string  `json:"type"`
			BaseURL         string  `json:"base_url"`
			APIKey          string  `json:"api_key"`
			Model           string  `json:"model"`
			UseProxy        *bool   `json:"use_proxy"`
			Reasoning       *bool   `json:"reasoning"`
			Fast            *bool   `json:"fast"`
			ContextWindow   *int    `json:"context_window"`
			MaxOutputTokens *int    `json:"max_output_tokens"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if body.DisplayName != nil {
			model := config.Model{
				DisplayName: *body.DisplayName,
				Type:        body.Type,
				BaseURL:     body.BaseURL,
				APIKey:      body.APIKey,
				Model:       body.Model,
				UseProxy:    body.UseProxy != nil && *body.UseProxy,
				Reasoning:   body.Reasoning != nil && *body.Reasoning,
				FastSupport: body.Fast != nil && *body.Fast,
			}
			if body.ContextWindow != nil {
				model.ContextWindow = *body.ContextWindow
			}
			if body.MaxOutputTokens != nil {
				model.MaxOutputTokens = *body.MaxOutputTokens
			}
			if err := backend.UpdateModel(r.PathValue("id"), model); err != nil {
				status := http.StatusBadRequest
				if err.Error() == "没有这个模型" {
					status = http.StatusNotFound
				}
				writeErr(w, status, err)
				return
			}
			writeState(w, backend)
			return
		}
		if body.Reasoning != nil {
			if err := backend.SetModelReasoning(r.PathValue("id"), *body.Reasoning); err != nil {
				writeErr(w, http.StatusNotFound, err)
				return
			}
		}
		if body.UseProxy != nil {
			if err := backend.SetModelProxy(r.PathValue("id"), *body.UseProxy); err != nil {
				writeErr(w, http.StatusNotFound, err)
				return
			}
		}
		if body.Fast != nil {
			if err := backend.SetModelFast(r.PathValue("id"), *body.Fast); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
		if (body.ContextWindow != nil && *body.ContextWindow < 0) || (body.MaxOutputTokens != nil && *body.MaxOutputTokens < 0) {
			writeErr(w, http.StatusBadRequest, i18n.E("token 数不能为负", "Token counts cannot be negative"))
			return
		}
		if body.ContextWindow != nil || body.MaxOutputTokens != nil {
			if err := backend.SetModelLimits(r.PathValue("id"), body.ContextWindow, body.MaxOutputTokens); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
		if body.UseProxy == nil && body.Reasoning == nil && body.Fast == nil && body.ContextWindow == nil && body.MaxOutputTokens == nil {
			writeErr(w, http.StatusBadRequest, errors.New("empty model update"))
			return
		}
		writeState(w, backend)
	})
	mux.HandleFunc("POST /api/models/{id}/test", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, backend.TestSaved(r.PathValue("id")))
	})
	mux.HandleFunc("POST /api/cursor", func(w http.ResponseWriter, r *http.Request) {
		if err := backend.OpenCursor(); err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		if err := backend.Quit(); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
	writeJSON(w, status, map[string]i18n.Text{"error": i18n.Of(err)})
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return i18n.E("请求内容无法解析", "Cannot parse the request")
	}
	return nil
}
