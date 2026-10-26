package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"

	"cursor-inner/internal/i18n"
	"cursor-inner/internal/provider"

	"cursor-inner/assets"
	"cursor-inner/internal/config"
)

//go:embed page.html
var page embed.FS

// Handler 不带版本号，标题只有程序名。
func Handler(backend Backend) http.Handler { return HandlerVersion(backend, "") }

// HandlerVersion 和 Handler 相同，页面 <title> 用 Title(version)，与窗口标题一致。
func HandlerVersion(backend Backend, version string) http.Handler {
	raw, _ := page.ReadFile("page.html")
	html := bytes.Replace(raw, []byte("<title>cursor-inner</title>"), []byte("<title>"+template.HTMLEscapeString(Title(version))+"</title>"), 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(html)
	})
	mux.HandleFunc("GET /icon.svg", func(w http.ResponseWriter, r *http.Request) {
		raw := assets.IconSVG
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": version})
	})
	registerDiscover(mux, backend)
	push := newStreams()
	if up, ok := backend.(UpdateBackend); ok {
		registerUpdate(mux, up, push)
	}
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
			Target  string `json:"target"`
			Enabled bool   `json:"enabled"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := backend.SetTakeover(body.Target, body.Enabled); err != nil {
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
	mux.HandleFunc("PUT /api/proxy/scope", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Scope string `json:"scope"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := backend.SetProxyScope(body.Scope); err != nil {
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
	mux.HandleFunc("PUT /api/desktop", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Background *bool `json:"background"`
			Notify     *bool `json:"notify"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := backend.SetDesktop(body.Background, body.Notify); err != nil {
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
		streamTest(w, func(progress func(provider.Progress)) provider.Result {
			return backend.TestDraft(model, progress)
		})
	})
	mux.HandleFunc("GET /api/models/{id}/key", func(w http.ResponseWriter, r *http.Request) {
		key, err := backend.ModelKey(r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"api_key": key})
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
			MaxImages       *int    `json:"max_images"`
			ImageBudgetMB   *int    `json:"image_budget_mb"`
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
			if body.MaxImages != nil {
				model.MaxImages = *body.MaxImages
			}
			if body.ImageBudgetMB != nil {
				model.ImageBudgetMB = *body.ImageBudgetMB
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
		streamTest(w, func(progress func(provider.Progress)) provider.Result {
			return backend.TestSaved(r.PathValue("id"), progress)
		})
	})
	mux.HandleFunc("POST /api/cursor", func(w http.ResponseWriter, r *http.Request) {
		if err := backend.OpenCursor(); err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("PUT /api/strict", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := backend.SetStrictEgress(body.Enabled); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("PUT /api/tunnel", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := backend.SetTunnel(body.Enabled); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/grok", func(w http.ResponseWriter, r *http.Request) {
		if err := backend.OpenGrok(); err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/codex", func(w http.ResponseWriter, r *http.Request) {
		if err := backend.OpenCodex(); err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/logs", func(w http.ResponseWriter, r *http.Request) {
		if err := backend.OpenLogs(); err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/leakcheck", func(w http.ResponseWriter, r *http.Request) {
		if err := backend.RunLeakCheck(); err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/quit/plan", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, backend.QuitPlan())
	})
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DontAsk bool `json:"dont_ask"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body) // 没有正文就是普通退出
		if err := backend.QuitConfirmed(body.DontAsk); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/restore-apps", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Dismiss bool `json:"dismiss"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		if err := backend.RestoreApps(body.Dismiss); err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	return &handler{Handler: guard(mux), push: push}
}

// testEvent 是测试接口的一行。第一行给 plan，之后是各阶段进度，最后一行带完整结果。
type testEvent struct {
	Plan   []string         `json:"plan,omitempty"`
	Stage  string           `json:"stage,omitempty"`
	State  string           `json:"state,omitempty"`
	OK     bool             `json:"ok,omitempty"`
	Tokens uint64           `json:"tokens,omitempty"`
	Result *provider.Result `json:"result,omitempty"`
}

// streamTest 把能力探测的进度按 NDJSON 逐行写出去，每写完一行就 flush，页面才能实时看到。
// 最终结果仍是同一个 provider.Result，和以前一次返回的 JSON 相同。
func streamTest(w http.ResponseWriter, run func(func(provider.Progress)) provider.Result) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, i18n.E("当前环境不支持流式响应", "Streaming is not supported here"))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	send := func(event testEvent) {
		if err := enc.Encode(event); err != nil {
			return
		}
		flusher.Flush()
	}
	result := run(func(p provider.Progress) {
		send(testEvent{Plan: p.Plan, Stage: p.Stage, State: p.State, OK: p.OK, Tokens: p.Tokens})
	})
	send(testEvent{Result: &result})
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
