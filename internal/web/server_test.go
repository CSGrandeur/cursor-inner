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
	strict     *bool
	tunnel     *bool
	view       View
	autostart  bool
	takeover   bool
	quit       bool
	opened     bool
	openedGrok bool
}

func (f *fake) State() (View, error) {
	f.view.Takeover = f.takeover
	f.view.Autostart.Enabled = f.autostart
	if f.view.Models == nil {
		f.view.Models = []ModelView{}
	}
	return f.view, nil
}
func (f *fake) SetTakeover(target string, enabled bool) error {
	if target == "grok" {
		f.view.TakeoverGrok = enabled
		return nil
	}
	f.takeover = enabled
	return nil
}
func (f *fake) SetProxy(bool, string) error { return nil }
func (f *fake) SetImage(baseURL, apiKey, model string) error {
	f.view.Image = ImageView{BaseURL: baseURL, Model: model, KeyHint: apiKey}
	return nil
}
func (f *fake) SetAutostart(enabled bool) error {
	f.autostart = enabled
	f.view.Autostart = autostart.State{Enabled: enabled, Mode: "logon-task", Detail: i18n.T("已写入", "Written")}
	return nil
}
func (f *fake) AddModel(config.Model) error            { return nil }
func (f *fake) UpdateModel(string, config.Model) error { return nil }
func (f *fake) DeleteModel(string) error               { return nil }
func (f *fake) SetModelProxy(string, bool) error       { return nil }
func (f *fake) SetModelReasoning(string, bool) error   { return nil }
func (f *fake) SetModelFast(string, bool) error        { return nil }
func (f *fake) SetModelLimits(_ string, contextWindow, maxOutput *int) error {
	if len(f.view.Models) == 0 {
		f.view.Models = []ModelView{{}}
	}
	if contextWindow != nil {
		f.view.Models[0].ContextWindow = *contextWindow
	}
	if maxOutput != nil {
		f.view.Models[0].MaxOutputTokens = *maxOutput
	}
	return nil
}
func (f *fake) TestDraft(config.Model) provider.Result {
	return provider.Result{OK: true, DurationMS: 1, TokensPerSecond: 1}
}
func (f *fake) TestSaved(string) provider.Result { return provider.Result{OK: true} }
func (f *fake) Quit() error                      { f.quit = true; return nil }
func (f *fake) OpenCursor() error                { f.opened = true; return nil }
func (f *fake) OpenGrok() error                  { f.openedGrok = true; return nil }
func (f *fake) OpenLogs() error                  { return nil }
func (f *fake) RunLeakCheck() error              { return nil }

func TestOpenCursorEndpoint(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	res, err := http.Post(srv.URL+"/api/cursor", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || !f.opened {
		t.Fatalf("status %d opened %v", res.StatusCode, f.opened)
	}
}

func TestOpenGrokEndpoint(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	res, err := http.Post(srv.URL+"/api/grok", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || !f.openedGrok {
		t.Fatalf("status %d opened %v", res.StatusCode, f.openedGrok)
	}
}

func TestLogsEndpoint(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	res, err := http.Post(srv.URL+"/api/logs", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestLeakCheckEndpoint(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	res, err := http.Post(srv.URL+"/api/leakcheck", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
}

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

func TestModelLimitsUpdate(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/models/m", strings.NewReader(`{"context_window":4000,"max_output_tokens":128}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || f.view.Models[0].ContextWindow != 4000 || f.view.Models[0].MaxOutputTokens != 128 {
		t.Fatalf("status %d %+v", res.StatusCode, f.view.Models)
	}
	req, _ = http.NewRequest(http.MethodPut, srv.URL+"/api/models/m", strings.NewReader(`{"context_window":-1}`))
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatal(res.StatusCode)
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
	if !strings.Contains(page, "开机启动") || !strings.Contains(page, ">接管 Cursor<") || !strings.Contains(page, ">接管 Grok<") || !strings.Contains(page, "reveal-key") || !strings.Contains(page, "上次测试") || !strings.Contains(page, "/icon.svg") || !strings.Contains(page, `id="image-url"`) || !strings.Contains(page, `name="context_window"`) || !strings.Contains(page, `id="open-cursor"`) || !strings.Contains(page, `id="open-grok"`) || !strings.Contains(page, `id="form-clear"`) || !strings.Contains(page, `data-i18n-tip="tipModel"`) || !strings.Contains(page, `href="https://github.com/CSGrandeur/cursor-inner"`) {
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
	for _, tc := range []struct {
		body string
		want bool
		grok bool
	}{
		{`{"target":"cursor","enabled":true}`, true, false},
		{`{"target":"grok","enabled":false}`, true, false},
	} {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/takeover", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		res3, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res3.Body.Close()
		if res3.StatusCode != 200 || f.takeover != tc.want || f.view.TakeoverGrok != tc.grok {
			t.Fatalf("%s status %d cursor %v grok %v", tc.body, res3.StatusCode, f.takeover, f.view.TakeoverGrok)
		}
	}
}
func (f *fake) SetStrictEgress(enabled bool) error { f.strict = &enabled; return nil }
func (f *fake) SetTunnel(enabled bool) error       { f.tunnel = &enabled; return nil }

func TestTunnelEndpoint(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/tunnel", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || f.tunnel == nil || !*f.tunnel {
		t.Fatalf("status %d tunnel %v", resp.StatusCode, f.tunnel)
	}
}

func TestStrictEndpoint(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/strict", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || f.strict == nil || !*f.strict {
		t.Fatalf("status %d strict %v", resp.StatusCode, f.strict)
	}
}
