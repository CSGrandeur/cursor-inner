package web

import (
	"context"
	"io"
	"net/http"

	"cursor-inner/internal/i18n"
)

type accountOps interface {
	List() []AntiAccountView
	LoginStart() (string, error)
	LoginState() AntiLoginState
	LoginCancel()
	Import(ctx context.Context, rawToken string) (AntiAccountView, error)
	ImportCurrent(ctx context.Context) (AntiAccountView, error)
	ImportJSON(ctx context.Context, raw []byte) ([]AntiImportResult, error)
	ExportJSON() ([]byte, error)
	Remove(id string) error
	Refresh(ctx context.Context, id string) (AntiAccountView, error)
	RefreshAll(ctx context.Context) (okCount, failCount int, firstErr error)
	Switch(ctx context.Context, id string) error
}

type antiAccountAdapter struct{ b AntiAccountBackend }

func (a antiAccountAdapter) List() []AntiAccountView     { return a.b.AntiAccountList() }
func (a antiAccountAdapter) LoginStart() (string, error) { return a.b.AntiAccountLoginStart() }
func (a antiAccountAdapter) LoginState() AntiLoginState  { return a.b.AntiAccountLoginState() }
func (a antiAccountAdapter) LoginCancel()                { a.b.AntiAccountLoginCancel() }
func (a antiAccountAdapter) ExportJSON() ([]byte, error) { return a.b.AntiAccountExportJSON() }
func (a antiAccountAdapter) Remove(id string) error      { return a.b.AntiAccountRemove(id) }
func (a antiAccountAdapter) Switch(c context.Context, i string) error {
	return a.b.AntiAccountSwitch(c, i)
}
func (a antiAccountAdapter) Import(c context.Context, rt string) (AntiAccountView, error) {
	return a.b.AntiAccountImport(c, rt)
}
func (a antiAccountAdapter) ImportCurrent(c context.Context) (AntiAccountView, error) {
	return a.b.AntiAccountImportCurrent(c)
}
func (a antiAccountAdapter) ImportJSON(c context.Context, raw []byte) ([]AntiImportResult, error) {
	return a.b.AntiAccountImportJSON(c, raw)
}
func (a antiAccountAdapter) Refresh(c context.Context, i string) (AntiAccountView, error) {
	return a.b.AntiAccountRefresh(c, i)
}
func (a antiAccountAdapter) RefreshAll(c context.Context) (int, int, error) {
	return a.b.AntiAccountRefreshAll(c)
}

type cursorAccountAdapter struct{ b CursorAccountBackend }

func (a cursorAccountAdapter) List() []AntiAccountView     { return a.b.CursorAccountList() }
func (a cursorAccountAdapter) LoginStart() (string, error) { return a.b.CursorAccountLoginStart() }
func (a cursorAccountAdapter) LoginState() AntiLoginState  { return a.b.CursorAccountLoginState() }
func (a cursorAccountAdapter) LoginCancel()                { a.b.CursorAccountLoginCancel() }
func (a cursorAccountAdapter) ExportJSON() ([]byte, error) { return a.b.CursorAccountExportJSON() }
func (a cursorAccountAdapter) Remove(id string) error      { return a.b.CursorAccountRemove(id) }
func (a cursorAccountAdapter) Switch(c context.Context, i string) error {
	return a.b.CursorAccountSwitch(c, i)
}
func (a cursorAccountAdapter) Import(c context.Context, rt string) (AntiAccountView, error) {
	return a.b.CursorAccountImport(c, rt)
}
func (a cursorAccountAdapter) ImportCurrent(c context.Context) (AntiAccountView, error) {
	return a.b.CursorAccountImportCurrent(c)
}
func (a cursorAccountAdapter) ImportJSON(c context.Context, raw []byte) ([]AntiImportResult, error) {
	return a.b.CursorAccountImportJSON(c, raw)
}
func (a cursorAccountAdapter) Refresh(c context.Context, i string) (AntiAccountView, error) {
	return a.b.CursorAccountRefresh(c, i)
}
func (a cursorAccountAdapter) RefreshAll(c context.Context) (int, int, error) {
	return a.b.CursorAccountRefreshAll(c)
}

func registerAntiAccounts(mux *http.ServeMux, backend AntiAccountBackend) {
	registerAccountRoutes(mux, "/api/antigravity/accounts", antiAccountAdapter{backend}, backend.(Backend))
}

func registerCursorAccounts(mux *http.ServeMux, backend CursorAccountBackend) {
	registerAccountRoutes(mux, "/api/cursor/accounts", cursorAccountAdapter{backend}, backend.(Backend))
}

// registerAccountRoutes 为反重力 / Cursor 统一挂账号管理路由。
func registerAccountRoutes(mux *http.ServeMux, prefix string, ops accountOps, stateBackend Backend) {
	mux.HandleFunc("GET "+prefix, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"accounts": ops.List()})
	})
	mux.HandleFunc("POST "+prefix+"/login", func(w http.ResponseWriter, r *http.Request) {
		url, err := ops.LoginStart()
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"auth_url": url})
	})
	mux.HandleFunc("GET "+prefix+"/login", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ops.LoginState())
	})
	mux.HandleFunc("DELETE "+prefix+"/login", func(w http.ResponseWriter, r *http.Request) {
		ops.LoginCancel()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST "+prefix+"/import", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		view, err := ops.Import(r.Context(), body.RefreshToken)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})
	mux.HandleFunc("POST "+prefix+"/import-current", func(w http.ResponseWriter, r *http.Request) {
		view, err := ops.ImportCurrent(r.Context())
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})
	mux.HandleFunc("POST "+prefix+"/import-json", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		defer r.Body.Close()
		if err != nil {
			writeErr(w, http.StatusBadRequest, i18n.E("请求内容读不出来", "Cannot read the request body"))
			return
		}
		results, err := ops.ImportJSON(r.Context(), raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	})
	mux.HandleFunc("GET "+prefix+"/export", func(w http.ResponseWriter, r *http.Request) {
		raw, err := ops.ExportJSON()
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("DELETE "+prefix+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := ops.Remove(r.PathValue("id")); err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeState(w, stateBackend)
	})
	mux.HandleFunc("POST "+prefix+"/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		view, err := ops.Refresh(r.Context(), r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})
	mux.HandleFunc("POST "+prefix+"/refresh-all", func(w http.ResponseWriter, r *http.Request) {
		okCount, failCount, firstErr := ops.RefreshAll(r.Context())
		if okCount == 0 && failCount > 0 && firstErr != nil {
			writeErr(w, http.StatusBadRequest, firstErr)
			return
		}
		stateView, err := stateBackend.State()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		var errText i18n.Text
		if firstErr != nil {
			errText = i18n.Of(firstErr)
		}
		writeJSON(w, http.StatusOK, struct {
			OK    int       `json:"ok"`
			Bad   int       `json:"bad"`
			Error i18n.Text `json:"error,omitzero"`
			State View      `json:"state"`
		}{OK: okCount, Bad: failCount, Error: errText, State: stateView})
	})
	mux.HandleFunc("POST "+prefix+"/{id}/switch", func(w http.ResponseWriter, r *http.Request) {
		if err := ops.Switch(r.Context(), r.PathValue("id")); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeState(w, stateBackend)
	})
}
