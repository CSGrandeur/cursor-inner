package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func (f *fake) QuitPlan() QuitPlan {
	return QuitPlan{Confirm: true, Closes: []string{"grok"}, Restore: []string{"codex"}}
}
func (f *fake) QuitConfirmed(dontAsk bool) error { quitDontAsk = &dontAsk; return f.Quit() }
func (f *fake) RestoreApps(dismiss bool) error   { restoreDismiss = &dismiss; return nil }

var quitDontAsk, restoreDismiss *bool

func quitReq(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestQuitPlanAndConfirmedQuitAndRestore(t *testing.T) {
	h := Handler(&fake{})
	rec := quitReq(t, h, "GET", "/api/quit/plan", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"closes":["grok"]`) || !strings.Contains(rec.Body.String(), `"restore":["codex"]`) {
		t.Fatalf("plan %d %s", rec.Code, rec.Body.String())
	}
	quitDontAsk = nil
	if rec := quitReq(t, h, "POST", "/api/quit", `{"dont_ask":true}`); rec.Code != 200 || quitDontAsk == nil || !*quitDontAsk {
		t.Fatalf("quit %d %s", rec.Code, rec.Body.String())
	}
	quitDontAsk = nil
	if rec := quitReq(t, h, "POST", "/api/quit", ``); rec.Code != 200 || quitDontAsk == nil || *quitDontAsk {
		t.Fatalf("quit without body %d %s", rec.Code, rec.Body.String())
	}
	if rec := quitReq(t, h, "POST", "/api/restore-apps", `{"dismiss":true}`); rec.Code != 200 || restoreDismiss == nil || !*restoreDismiss {
		t.Fatalf("restore %d %s", rec.Code, rec.Body.String())
	}
}
