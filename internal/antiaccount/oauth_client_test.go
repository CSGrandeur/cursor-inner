package antiaccount

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testGoogleID(seed string) string {
	return "1234567890123-" + strings.Repeat(seed, 24) + ".apps.googleusercontent.com"
}

func testGoogleCred(seed string) string {
	return "GOCSPX-" + strings.Repeat(seed, 28)
}

func TestExtractPersonalClientIDFromAsarAndChunkBoundary(t *testing.T) {
	personalID := testGoogleID("a")
	businessID := testGoogleID("b")

	// 1. 跨 1 MiB 块边界切开 SIGN_IN_CLIENTS 与 clientId
	resDir := filepath.Join(t.TempDir(), "resources")
	if err := os.MkdirAll(resDir, 0o755); err != nil {
		t.Fatal(err)
	}
	buf := bytes.Repeat([]byte("z"), clientIDChunkSize-32)
	buf = append(buf, []byte("const SIGN_IN_CLIENTS = [{ clientId: '"+personalID+"', scopes: ['aicode'] }, { clientId: '"+businessID+"' }];")...)
	if err := os.WriteFile(filepath.Join(resDir, "app.asar"), buf, 0o644); err != nil {
		t.Fatal(err)
	}

	got, _, _, _, ok := extractPersonalClientID(resDir)
	if !ok || got != personalID {
		t.Fatalf("got=%q ok=%v want=%q", got, ok, personalID)
	}

	// 2. 无 app.asar 时回退扫描 app/extensions JS 文件
	resDirJS := filepath.Join(t.TempDir(), "resources")
	jsPath := filepath.Join(resDirJS, "app", "extensions", "antigravity", "dist", "main.js")
	if err := os.MkdirAll(filepath.Dir(jsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	js := "const other='" + businessID + "'; const c='" + personalID + "'; const s='https://www.googleapis.com/auth/aicode';"
	if err := os.WriteFile(jsPath, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	gotJS, _, _, _, ok := extractPersonalClientID(resDirJS)
	if !ok || gotJS != personalID {
		t.Fatalf("js fallback got=%q ok=%v want=%q", gotJS, ok, personalID)
	}
}

func TestScanLanguageServerCredsPackedAndChunkBoundary(t *testing.T) {
	credA := testGoogleCred("A")
	credB := testGoogleCred("B")

	binPath := filepath.Join(t.TempDir(), "language_server")
	// 把 credA 跨过 1 MiB 边界，再把 credB 按 Go .rodata 紧凑拼接（无空字符终止）。
	data := bytes.Repeat([]byte("x"), credChunkSize-12)
	data = append(data, []byte(credA)...)
	data = append(data, bytes.Repeat([]byte("y"), 128)...)
	data = append(data, []byte("s/%s:generateContent"+credB+"decoding in-band SSE")...)
	if err := os.WriteFile(binPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := scanLanguageServerCreds(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != credA || got[1] != credB {
		t.Fatalf("got=%v want=[%s %s]", got, credA, credB)
	}
}

func TestResolveOAuthClientMatchCacheAndRetry(t *testing.T) {
	personalID := testGoogleID("p")
	wrongCred := testGoogleCred("w")
	rightCred := testGoogleCred("r")
	rotatedCred := testGoogleCred("n")

	resDir := filepath.Join(t.TempDir(), "resources")
	binDir := filepath.Join(resDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	asarData := []byte("const SIGN_IN_CLIENTS=[{clientId:'" + personalID + "'}];")
	if err := os.WriteFile(filepath.Join(resDir, "app.asar"), asarData, 0o644); err != nil {
		t.Fatal(err)
	}
	lsName := "language_server"
	if runtime.GOOS == "windows" {
		lsName = "language_server.exe"
	}
	lsPath := filepath.Join(binDir, lsName)
	if err := os.WriteFile(lsPath, []byte("head"+wrongCred+"mid"+rightCred+"tail"), 0o644); err != nil {
		t.Fatal(err)
	}

	var probeCalls atomic.Int32
	var activeCred atomic.Value
	activeCred.Store(rightCred)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotID := r.Form.Get("client_id")
		gotCred := r.Form.Get("client_secret")
		rt := r.Form.Get("refresh_token")
		if gotID != personalID || gotCred != activeCred.Load().(string) {
			if rt == "check" {
				probeCalls.Add(1)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		if rt == "check" {
			probeCalls.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-" + gotCred,
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	oldTokURL := googleTokenURL
	googleTokenURL = srv.URL
	t.Cleanup(func() { googleTokenURL = oldTokURL })

	dataDir := t.TempDir()
	s, err := LoadStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(s, nil)
	m.SetResourcesDirs(func(string) []string { return []string{resDir} })

	// 1. LoginStart 需要的 client_id 解析不走网络
	cid, err := m.resolveClientID()
	if err != nil || cid != personalID {
		t.Fatalf("resolveClientID cid=%q err=%v", cid, err)
	}
	if probeCalls.Load() != 0 {
		t.Fatalf("resolveClientID must not hit network, got %d calls", probeCalls.Load())
	}

	// 2. 首次 resolveOAuthClient：匹配两个候选项并落盘缓存
	res, err := m.resolveOAuthClient(context.Background(), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if res.client.ID != personalID || res.client.Cred != rightCred || res.fromCache {
		t.Fatalf("unexpected resolved: %+v", res)
	}
	if probeCalls.Load() != 2 {
		t.Fatalf("expected 2 probe calls, got %d", probeCalls.Load())
	}
	cachePath := filepath.Join(dataDir, oauthCacheFile)
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("cache file mode %o, want 600", info.Mode().Perm())
	}

	// 3. 新建 Manager 从磁盘缓存读取，不再发网络探测
	m2 := NewManager(s, nil)
	m2.SetResourcesDirs(func(string) []string { return []string{resDir} })
	res2, err := m2.resolveOAuthClient(context.Background(), srv.Client())
	if err != nil || !res2.fromCache || res2.client.Cred != rightCred {
		t.Fatalf("cache reload failed: res2=%+v err=%v", res2, err)
	}
	if probeCalls.Load() != 2 {
		t.Fatalf("cache hit should not probe again, got %d calls", probeCalls.Load())
	}

	// 4. 篡改缓存（模拟极端情况缓存参数被拒）→ refreshToken 自动清缓存重探一次并成功
	rawCache, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	var c cachedOAuthClient
	if err := json.Unmarshal(rawCache, &c); err != nil {
		t.Fatal(err)
	}
	c.ClientCred = wrongCred
	badRaw, _ := json.Marshal(c)
	if err := os.WriteFile(cachePath, badRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	m3 := NewManager(s, nil)
	m3.SetResourcesDirs(func(string) []string { return []string{resDir} })
	tok, err := m3.refreshToken(context.Background(), srv.Client(), "rt-user")
	if err != nil {
		t.Fatalf("expected retry after cache invalidation to succeed: %v", err)
	}
	if tok.AccessToken != "at-"+rightCred {
		t.Fatalf("unexpected access token %q", tok.AccessToken)
	}

	// 5. 升级 language_server（文件大小或 mtime 变化）→ 自动重新提取与匹配
	activeCred.Store(rotatedCred)
	lsData := []byte("head" + wrongCred + "mid" + rotatedCred + "tail-v2")
	if err := os.WriteFile(lsPath, lsData, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(lsPath, time.Now().Add(2*time.Second), time.Now().Add(2*time.Second))
	res3, err := m3.resolveOAuthClient(context.Background(), srv.Client())
	if err != nil || res3.client.Cred != rotatedCred || res3.fromCache {
		t.Fatalf("expected re-extract after LS update, got res3=%+v err=%v", res3, err)
	}
}

func TestResolveOAuthClientMissingAndEnvOverride(t *testing.T) {
	s, err := LoadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(s, nil)
	m.SetResourcesDirs(func(string) []string { return nil })

	if _, err := m.resolveClientID(); err == nil {
		t.Fatal("expected error when Antigravity is not installed")
	}
	if _, err := m.resolveOAuthClient(context.Background(), http.DefaultClient); err == nil {
		t.Fatal("expected error when Antigravity is not installed")
	}

	wantID := testGoogleID("e")
	wantCred := testGoogleCred("E")
	t.Setenv(envOAuthClientID, wantID)
	t.Setenv(envOAuthClientCred, wantCred)

	cid, err := m.resolveClientID()
	if err != nil || cid != wantID {
		t.Fatalf("env resolveClientID got %q err=%v", cid, err)
	}
	res, err := m.resolveOAuthClient(context.Background(), http.DefaultClient)
	if err != nil || res.client.ID != wantID || res.client.Cred != wantCred || !res.fromFixed {
		t.Fatalf("env resolveOAuthClient got %+v err=%v", res, err)
	}
}
