package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursor-inner/internal/autostart"
	"cursor-inner/internal/config"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/provider"
)

type fake struct {
	view      View
	autostart bool
	takeover  bool
	quit      bool
}

func (f *fake) State() (View, error) {
	f.view.Takeover = f.takeover
	f.view.Autostart.Enabled = f.autostart
	if f.view.Models == nil {
		f.view.Models = []ModelView{}
	}
	return f.view, nil
}
func (f *fake) SetTakeover(enabled bool) error { f.takeover = enabled; return nil }
func (f *fake) SetProxy(bool, string) error    { return nil }
func (f *fake) SetAutostart(enabled bool) error {
	f.autostart = enabled
	f.view.Autostart = autostart.State{Enabled: enabled, Mode: "logon-task", Detail: i18n.T("已写入", "Written")}
	return nil
}
func (f *fake) AddModel(config.Model) error      { return nil }
func (f *fake) DeleteModel(string) error         { return nil }
func (f *fake) SetModelProxy(string, bool) error { return nil }
func (f *fake) TestDraft(config.Model) provider.Result {
	return provider.Result{OK: true, DurationMS: 1, TokensPerSecond: 1}
}
func (f *fake) TestSaved(string) provider.Result { return provider.Result{OK: true} }
func (f *fake) Quit() error                      { f.quit = true; return nil }

func TestQuitEndpoint(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	res, err := http.Post(srv.URL+"/api/quit", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || !f.quit {
		t.Fatalf("status %d quit %v", res.StatusCode, f.quit)
	}
}

func TestPageAndAutostartToggle(t *testing.T) {
	f := &fake{view: View{ListenURL: "http://127.0.0.1:9", CA: "missing"}}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body := make([]byte, 1<<20)
	n, _ := res.Body.Read(body)
	page := string(body[:n])
	if !strings.Contains(page, "开机启动") || !strings.Contains(page, ">接管<") || !strings.Contains(page, "reveal-key") || !strings.Contains(page, "上次测试") || !strings.Contains(page, "/icon.svg") {
		t.Fatalf("page missing sections n=%d head=%q", n, page[:min(180, n)])
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/autostart", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != 200 || !f.autostart {
		t.Fatalf("status %d enabled %v", res2.StatusCode, f.autostart)
	}
}
