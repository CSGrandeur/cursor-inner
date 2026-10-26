package web

import (
	"encoding/json"
	"io"
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
	strict        *bool
	scope         string
	tunnel        *bool
	view          View
	autostart     bool
	takeover      bool
	quit          bool
	opened        bool
	openedGrok    bool
	openedCodex   bool
	discover      []string
	discoverErr   error
	discoverType  string
	discoverURL   string
	discoverKey   string
	discoverID    string
	discoverProxy bool
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
	switch target {
	case "grok":
		f.view.TakeoverGrok = enabled
		return nil
	case "codex":
		f.view.TakeoverCodex = enabled
		return nil
	}
	f.takeover = enabled
	return nil
}
func (f *fake) SetProxy(bool, string) error { return nil }
func (f *fake) SetProxyScope(scope string) error {
	f.scope = scope
	return nil
}
func (f *fake) SetImage(baseURL, apiKey, model string) error {
	f.view.Image = ImageView{BaseURL: baseURL, Model: model, KeyHint: apiKey}
	return nil
}
func (f *fake) SetAutostart(enabled bool) error {
	f.autostart = enabled
	f.view.Autostart = autostart.State{Enabled: enabled, Mode: "logon-task", Detail: i18n.T("已写入", "Written")}
	return nil
}
func (f *fake) SetDesktop(background, notify *bool) error {
	if background != nil {
		f.view.Desktop.Background = *background
	}
	if notify != nil {
		f.view.Desktop.Notify = *notify
	}
	return nil
}
func (f *fake) AddModel(config.Model) error            { return nil }
func (f *fake) UpdateModel(string, config.Model) error { return nil }
func (f *fake) DeleteModel(string) error               { return nil }
func (f *fake) ModelKey(id string) (string, error) {
	if id != "m1" {
		return "", i18n.E("没有这个模型", "No such model")
	}
	return "sk-test", nil
}

func (f *fake) DiscoverModels(modelType, baseURL, apiKey string, useProxy bool, reuseID string) ([]string, error) {
	f.discoverType = modelType
	f.discoverURL = baseURL
	f.discoverKey = apiKey
	f.discoverProxy = useProxy
	f.discoverID = reuseID
	if f.discoverErr != nil {
		return nil, f.discoverErr
	}
	if f.discover == nil {
		return []string{}, nil
	}
	return append([]string(nil), f.discover...), nil
}
func (f *fake) SetModelProxy(string, bool) error     { return nil }
func (f *fake) SetModelReasoning(string, bool) error { return nil }
func (f *fake) SetModelFast(string, bool) error      { return nil }
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
func (f *fake) TestDraft(_ config.Model, progress func(provider.Progress)) provider.Result {
	return fakeTestProgress(progress)
}
func (f *fake) TestSaved(_ string, progress func(provider.Progress)) provider.Result {
	return fakeTestProgress(progress)
}

func fakeTestProgress(progress func(provider.Progress)) provider.Result {
	if progress != nil {
		progress(provider.Progress{Plan: []string{provider.StageTools, provider.StageSpeed}})
		progress(provider.Progress{Stage: provider.StageTools, State: "start"})
		progress(provider.Progress{Stage: provider.StageTools, State: "done", OK: true})
		progress(provider.Progress{Stage: provider.StageSpeed, State: "start"})
		progress(provider.Progress{Stage: provider.StageSpeed, State: "tokens", Tokens: 3})
	}
	return provider.Result{OK: true, DurationMS: 1, TokensPerSecond: 1}
}
func (f *fake) Quit() error         { f.quit = true; return nil }
func (f *fake) OpenCursor() error   { f.opened = true; return nil }
func (f *fake) OpenGrok() error     { f.openedGrok = true; return nil }
func (f *fake) OpenCodex() error    { f.openedCodex = true; return nil }
func (f *fake) OpenLogs() error     { return nil }
func (f *fake) RunLeakCheck() error { return nil }

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

func TestModelKeyEndpoint(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/models/m1/key")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body := make([]byte, 256)
	n, _ := res.Body.Read(body)
	if res.StatusCode != 200 || !strings.Contains(string(body[:n]), `"sk-test"`) {
		t.Fatalf("status %d body %s", res.StatusCode, body[:n])
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control %q", got)
	}
	missing, err := http.Get(srv.URL + "/api/models/nope/key")
	if err != nil {
		t.Fatal(err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing model: status %d", missing.StatusCode)
	}
}

func TestModelTestStreamsProgress(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	res, err := http.Post(srv.URL+"/api/models/m1/test", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/x-ndjson; charset=utf-8" {
		t.Fatalf("status %d type %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var events []testEvent
	dec := json.NewDecoder(res.Body)
	for {
		var ev testEvent
		if err := dec.Decode(&ev); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	if len(events) != 6 {
		t.Fatalf("want 6 events, got %d: %+v", len(events), events)
	}
	if len(events[0].Plan) != 2 || events[0].Plan[0] != provider.StageTools {
		t.Fatalf("plan %+v", events[0])
	}
	if events[4].State != "tokens" || events[4].Tokens != 3 {
		t.Fatalf("token event %+v", events[4])
	}
	if last := events[len(events)-1]; last.Result == nil || !last.Result.OK {
		t.Fatalf("result %+v", last)
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
	if !strings.Contains(page, "开机启动") || !strings.Contains(page, ">接管 Cursor<") || !strings.Contains(page, ">接管 Grok<") || !strings.Contains(page, ">接管 Codex<") || !strings.Contains(page, "reveal-key") || !strings.Contains(page, "::-ms-reveal") || !strings.Contains(page, "上次测试") || !strings.Contains(page, "/icon.svg") || !strings.Contains(page, `id="image-url"`) || !strings.Contains(page, `name="context_window"`) || !strings.Contains(page, `id="open-cursor"`) || !strings.Contains(page, `id="open-grok"`) || !strings.Contains(page, `id="open-codex"`) || !strings.Contains(page, `id="form-clear"`) || !strings.Contains(page, `data-i18n-tip="tipModel"`) || !strings.Contains(page, `data-help="context"`) || !strings.Contains(page, `data-help="maxOut"`) || !strings.Contains(page, `href="https://github.com/CSGrandeur/cursor-inner"`) {
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
		{`{"target":"codex","enabled":true}`, true, false},
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
	if !f.view.TakeoverCodex {
		t.Fatal("codex takeover was not stored")
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

func TestProxyScopeEndpoint(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/proxy/scope", strings.NewReader(`{"scope":"official"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || f.scope != "official" {
		t.Fatalf("status %d scope %q", res.StatusCode, f.scope)
	}
}

func TestDesktopSettingsOnlyChangeWhatIsSent(t *testing.T) {
	f := &fake{view: View{Desktop: DesktopView{Tray: true, Notify: true}}}
	srv := httptest.NewServer(Handler(f))
	defer srv.Close()
	put := func(body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/desktop", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", srv.URL)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	if res := put(`{"background":true}`); res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	if !f.view.Desktop.Background || !f.view.Desktop.Notify {
		t.Fatalf("background only: %+v", f.view.Desktop)
	}
	put(`{"notify":false}`)
	if !f.view.Desktop.Background || f.view.Desktop.Notify {
		t.Fatalf("notify only: %+v", f.view.Desktop)
	}
}
