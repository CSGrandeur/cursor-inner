package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursor-inner/internal/i18n"
)

type fakeAnti struct {
	fake
	order         []string
	accounts      []AntiAccountView
	cursorAccs    []AntiAccountView
	switchedID    string
	curSwitchedID string
	refreshOK     int
	refreshFail   int
	refreshErr    error
}

func (f *fakeAnti) State() (View, error) {
	v, err := f.fake.State()
	if len(f.order) > 0 {
		v.AccountOrder = append([]string(nil), f.order...)
	}
	if len(f.accounts) == 0 {
		v.AntiAccounts = []AntiAccountView{}
	} else {
		v.AntiAccounts = append([]AntiAccountView(nil), f.accounts...)
	}
	if f.cursorAccs == nil {
		v.CursorAccounts = []AntiAccountView{}
	} else {
		v.CursorAccounts = append([]AntiAccountView(nil), f.cursorAccs...)
	}
	return v, err
}

func (f *fakeAnti) AntiAccountList() []AntiAccountView { return f.accounts }
func (f *fakeAnti) AntiAccountLoginStart() (string, error) {
	return "https://accounts.google.com/mock", nil
}
func (f *fakeAnti) AntiAccountLoginState() AntiLoginState { return AntiLoginState{} }
func (f *fakeAnti) AntiAccountLoginCancel()               {}
func (f *fakeAnti) AntiAccountImport(_ context.Context, rt string) (AntiAccountView, error) {
	v := AntiAccountView{ID: "id-1", Email: "one@example.com", Tier: "FREE"}
	if rt == "" {
		return v, i18n.E("空", "empty")
	}
	f.accounts = append(f.accounts, v)
	return v, nil
}
func (f *fakeAnti) AntiAccountImportCurrent(context.Context) (AntiAccountView, error) {
	return AntiAccountView{}, nil
}
func (f *fakeAnti) AntiAccountImportJSON(context.Context, []byte) ([]AntiImportResult, error) {
	return []AntiImportResult{{Email: "one@example.com", OK: true}}, nil
}
func (f *fakeAnti) AntiAccountExportJSON() ([]byte, error) {
	return []byte(`{"accounts":[]}`), nil
}
func (f *fakeAnti) AntiAccountRemove(id string) error {
	next := f.accounts[:0]
	for _, a := range f.accounts {
		if a.ID != id {
			next = append(next, a)
		}
	}
	f.accounts = next
	return nil
}
func (f *fakeAnti) AntiAccountRefresh(_ context.Context, id string) (AntiAccountView, error) {
	return AntiAccountView{ID: id, Email: "one@example.com", Tier: "PRO"}, nil
}
func (f *fakeAnti) AntiAccountRefreshAll(context.Context) (int, int, error) {
	return f.refreshOK, f.refreshFail, f.refreshErr
}
func (f *fakeAnti) AntiAccountSwitch(_ context.Context, id string) error {
	f.switchedID = id
	return nil
}

func (f *fakeAnti) CursorAccountList() []AntiAccountView { return f.cursorAccs }
func (f *fakeAnti) CursorAccountLoginStart() (string, error) {
	return "https://cursor.com/loginDeepControl?mock=1", nil
}
func (f *fakeAnti) CursorAccountLoginState() AntiLoginState { return AntiLoginState{} }
func (f *fakeAnti) CursorAccountLoginCancel()               {}
func (f *fakeAnti) CursorAccountImport(_ context.Context, rt string) (AntiAccountView, error) {
	v := AntiAccountView{ID: "cid-1", Email: "cur@example.com", Tier: "PRO+"}
	if rt == "" {
		return v, i18n.E("空", "empty")
	}
	f.cursorAccs = append(f.cursorAccs, v)
	return v, nil
}
func (f *fakeAnti) CursorAccountImportCurrent(context.Context) (AntiAccountView, error) {
	return AntiAccountView{}, nil
}
func (f *fakeAnti) CursorAccountImportJSON(context.Context, []byte) ([]AntiImportResult, error) {
	return []AntiImportResult{{Email: "cur@example.com", OK: true}}, nil
}
func (f *fakeAnti) CursorAccountExportJSON() ([]byte, error) {
	return []byte(`{"accounts":[]}`), nil
}
func (f *fakeAnti) CursorAccountRemove(id string) error {
	next := f.cursorAccs[:0]
	for _, a := range f.cursorAccs {
		if a.ID != id {
			next = append(next, a)
		}
	}
	f.cursorAccs = next
	return nil
}
func (f *fakeAnti) CursorAccountRefresh(_ context.Context, id string) (AntiAccountView, error) {
	return AntiAccountView{ID: id, Email: "cur@example.com", Tier: "ULTRA"}, nil
}
func (f *fakeAnti) CursorAccountRefreshAll(context.Context) (int, int, error) {
	return len(f.cursorAccs), 0, nil
}
func (f *fakeAnti) CursorAccountSwitch(_ context.Context, id string) error {
	f.curSwitchedID = id
	return nil
}

func newLocalReq(method, path string, body []byte) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = "127.0.0.1:8765"
	r.RemoteAddr = "127.0.0.1:12345"
	return r
}

func TestStateSerializesEmptyAccountsArraysVsUnsupportedNull(t *testing.T) {
	hSupported := Handler(&fakeAnti{order: []string{"antigravity", "cursor"}, accounts: []AntiAccountView{}})
	rec := httptest.NewRecorder()
	hSupported.ServeHTTP(rec, newLocalReq(http.MethodGet, "/api/state", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"anti_accounts":[]`) || !strings.Contains(body, `"cursor_accounts":[]`) {
		t.Fatalf("empty account lists must serialize as [], got: %s", body)
	}
	if !strings.Contains(body, `"account_order":["antigravity","cursor"]`) {
		t.Fatalf("account_order missing: %s", body)
	}

	hUnsupported := Handler(&fake{})
	rec2 := httptest.NewRecorder()
	hUnsupported.ServeHTTP(rec2, newLocalReq(http.MethodGet, "/api/state", nil))
	body2 := rec2.Body.String()
	if !strings.Contains(body2, `"anti_accounts":null`) || !strings.Contains(body2, `"cursor_accounts":null`) {
		t.Fatalf("unsupported backend must serialize account lists as null, got: %s", body2)
	}
}

func TestAccountRoutesForBothAntigravityAndCursor(t *testing.T) {
	b := &fakeAnti{
		accounts: []AntiAccountView{
			{ID: "a1", Email: "one@example.com", Tier: "PRO", Quotas: []AntiQuotaView{{Label: "Gemini", Percent: 90}}},
		},
		cursorAccs: []AntiAccountView{
			{ID: "c1", Email: "cur@example.com", Tier: "ULTRA", Quotas: []AntiQuotaView{{Label: "Plan", Percent: 78}}},
		},
		refreshOK: 1,
	}
	h := Handler(b)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newLocalReq(http.MethodPost, "/api/antigravity/accounts/refresh-all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh-all status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res struct {
		OK    int  `json:"ok"`
		Bad   int  `json:"bad"`
		State View `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.OK != 1 || res.Bad != 0 || len(res.State.AntiAccounts) != 1 || len(res.State.CursorAccounts) != 1 {
		t.Fatalf("unexpected refresh-all response: %+v", res)
	}

	recImp := httptest.NewRecorder()
	h.ServeHTTP(recImp, newLocalReq(http.MethodPost, "/api/cursor/accounts/import-json", []byte(`[{"refresh_token":"jwt"}]`)))
	if recImp.Code != http.StatusOK || !strings.Contains(recImp.Body.String(), `"cur@example.com"`) {
		t.Fatalf("cursor import-json status=%d body=%s", recImp.Code, recImp.Body.String())
	}

	recSw := httptest.NewRecorder()
	h.ServeHTTP(recSw, newLocalReq(http.MethodPost, "/api/cursor/accounts/c1/switch", nil))
	if recSw.Code != http.StatusOK || b.curSwitchedID != "c1" {
		t.Fatalf("cursor switch status=%d curSwitchedID=%q", recSw.Code, b.curSwitchedID)
	}

	recDel := httptest.NewRecorder()
	h.ServeHTTP(recDel, newLocalReq(http.MethodDelete, "/api/cursor/accounts/c1", nil))
	if recDel.Code != http.StatusOK || len(b.cursorAccs) != 0 {
		t.Fatalf("cursor delete status=%d remaining=%d", recDel.Code, len(b.cursorAccs))
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, newLocalReq(http.MethodPost, "/api/antigravity/accounts/a1/switch", nil))
	if rec2.Code != http.StatusOK || b.switchedID != "a1" {
		t.Fatalf("anti switch status=%d switchedID=%q", rec2.Code, b.switchedID)
	}
}
