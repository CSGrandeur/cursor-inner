package antiaccount

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreUpsertMergesByEmail(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.upsert("A@Example.com", "Ann", Token{RefreshToken: "r1", AccessToken: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.upsert(" a@example.com ", "", Token{RefreshToken: "r2", AccessToken: "a2"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Token.RefreshToken != "r2" || second.Name != "Ann" {
		t.Fatalf("merge: first=%+v second=%+v", first, second)
	}
	if err := s.remove(first.ID); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.List()) != 0 {
		t.Fatalf("remove did not persist: %+v", reloaded.List())
	}
	info, err := os.Stat(filepath.Join(dir, "antigravity-accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("accounts file mode %o, want 600", info.Mode().Perm())
	}
}

func TestInjectFillsMissingKeysAndKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	extra := topicWithEntry(nil, "otherSentinel", []byte("keep-me"))
	oauth := topicWithEntry(extra, oauthSentinelKey, oauthInfo(Token{AccessToken: "old", RefreshToken: "old-r", Expiry: 10}))
	if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?), (?, ?), (?, ?)`,
		"filler", []byte("stay"),
		keyLegacyInit, []byte("stale-user"),
		keyEnterprise, []byte("old-project"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?)`, keyUnifiedOAuth, []byte(base64.StdEncoding.EncodeToString(oauth))); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	a := Account{
		Email: "person@example.com",
		Name:  "Person",
		Token: Token{AccessToken: "a-new", RefreshToken: "r-new", IDToken: "i-new", Expiry: 1_700_000_000},
	}
	if err := injectAccount(path, a); err != nil {
		t.Fatal(err)
	}

	bak, err := os.ReadFile(path + ".cursor-inner-bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(bak) == 0 {
		t.Fatal("backup is empty")
	}
	old, err := sql.Open("sqlite", path+".cursor-inner-bak")
	if err != nil {
		t.Fatal(err)
	}
	var legacy string
	if err := old.QueryRow(`SELECT value FROM ItemTable WHERE key=?`, keyLegacyInit).Scan(&legacy); err != nil || legacy != "stale-user" {
		t.Fatalf("backup lost the pre-switch legacy value: %v %q", err, legacy)
	}
	_ = old.Close()

	email, name, token, ok, err := CurrentFromDB(path)
	if err != nil || !ok {
		t.Fatalf("re-read ok=%v err=%v", ok, err)
	}
	if email != a.Email || name != a.Name || token.AccessToken != a.Token.AccessToken || token.RefreshToken != a.Token.RefreshToken || token.IDToken != a.Token.IDToken {
		t.Fatalf("token mismatch email=%s name=%s token=%+v", email, name, token)
	}
	got, err := readStateDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	if v, ok := got.Get("filler"); !ok || string(v) != "stay" {
		t.Fatalf("filler = %q ok=%v", v, ok)
	}
	if _, ok := got.Get(keyLegacyInit); ok {
		t.Fatal("legacy key was not deleted")
	}
	if _, ok := got.Get(keyEnterprise); ok {
		t.Fatal("enterprise preference was not deleted")
	}
	if v, ok := got.Get(keyOnboarding); !ok || string(v) != "true" {
		t.Fatalf("onboarding = %q ok=%v", v, ok)
	}
	raw, ok := got.Get(keyUnifiedOAuth)
	if !ok {
		t.Fatal("oauth key missing")
	}
	topic, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var sawOther bool
	for _, e := range parseTopic(topic) {
		if e.key == "otherSentinel" && string(e.payload) == "keep-me" {
			sawOther = true
		}
	}
	if !sawOther {
		t.Fatal("other sentinel in the oauth topic was dropped")
	}
}

func TestSwitchOrder(t *testing.T) {
	oldSettle := postStopSettle
	postStopSettle = 0
	t.Cleanup(func() { postStopSettle = oldSettle })

	dir := t.TempDir()
	s, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	account, err := s.upsert("person@example.com", "Person", Token{
		AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Unix() + 7200,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(s, nil)
	var order []string
	running := true
	m.SetHooks(
		func() error { order = append(order, "stop"); running = false; return nil },
		func() error { order = append(order, "start"); return nil },
		func() bool { return running },
		nil,
	)
	m.applyCreds = func(Account) error { order = append(order, "write"); return nil }
	if err := m.Switch(context.Background(), account.ID); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "stop,write,start" {
		t.Fatalf("order %s", got)
	}
	if s.CurrentID() != account.ID {
		t.Fatalf("current %s", s.CurrentID())
	}
}

func TestSwitchDoesNotWriteWhenStopFails(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	account, err := s.upsert("person@example.com", "", Token{
		AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Unix() + 7200,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(s, nil)
	wrote := false
	m.SetHooks(
		func() error { return os.ErrPermission },
		func() error { t.Fatal("started after a failed stop"); return nil },
		func() bool { return true },
		nil,
	)
	m.applyCreds = func(Account) error { wrote = true; return nil }
	if err := m.Switch(context.Background(), account.ID); err == nil {
		t.Fatal("expected stop error")
	}
	if wrote {
		t.Fatal("credentials were written after stop failed")
	}
}

func TestAuthURLUsesIDEClient(t *testing.T) {
	u := authURL("cid-test", "http://127.0.0.1:1/callback", "state-1")
	for _, want := range []string{"cid-test", "cloud-platform", "cclog", "experimentsandconfigs", "access_type=offline", "prompt=consent", "state=state-1"} {
		if !strings.Contains(u, want) {
			t.Fatalf("auth URL missing %s: %s", want, u)
		}
	}
	if strings.Contains(u, "client_secret") {
		t.Fatal("auth URL must not carry the client credential")
	}
}

func TestStoreBOMAndQuotaPreservedAcrossUpsert(t *testing.T) {
	dir := t.TempDir()
	bomJSON := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"accounts":[{"id":"id1","email":"a@example.com","token":{"access_token":"a1","refresh_token":"r1","expiry":10},"quota":{"tier":"PRO","buckets":[{"label":"Gemini","percent":80}]},"status":"invalid_grant"}]}`)...)
	if err := os.WriteFile(filepath.Join(dir, "antigravity-accounts.json"), bomJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := s.upsert("A@Example.com", "", Token{AccessToken: "a2", RefreshToken: "r2", Expiry: 20})
	if err != nil {
		t.Fatal(err)
	}
	if acc.Status != "" {
		t.Fatalf("expected status cleared after upsert, got %q", acc.Status)
	}
	if acc.Quota == nil || acc.Quota.Tier != "PRO" || len(acc.Quota.Buckets) != 1 || acc.Quota.Buckets[0].Percent != 80 {
		t.Fatalf("expected existing quota preserved on token re-import, got %+v", acc.Quota)
	}
}

func TestWSLHomeResolutionAndFallbackFiles(t *testing.T) {
	root := t.TempDir()
	if d := parseWSLStateDistro([]byte(`{"distro":"Ubuntu-26.04"}`)); d != "Ubuntu-26.04" {
		t.Fatalf("distro=%q", d)
	}
	if d := parseWSLStateDistro([]byte(`{"distro":"../bad"}`)); d != "" {
		t.Fatalf("expected traversal distro rejected, got %q", d)
	}

	userHome := filepath.Join(root, "home", "alice")
	bobHome := filepath.Join(root, "home", "bob")
	for _, d := range []string{filepath.Join(root, "etc"), userHome, bobHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "wsl.conf"), []byte("[user]\ndefault = bob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hp := "/" + "home/"
	passwd := "root:x:0:0:root:/var/root:/bin/bash\nalice:x:1000:1000::" + hp + "alice:/bin/bash\nbob:x:1001:1001::" + hp + "bob:/bin/bash\n"
	if err := os.WriteFile(filepath.Join(root, "etc", "passwd"), []byte(passwd), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveWSLHomeFromRoot(root); got != bobHome {
		t.Fatalf("want wsl.conf default user home %q, got %q", bobHome, got)
	}

	// 写入 jetski 凭据时保留已有的 writer_id，并以 0600 权限落盘。
	gemDir := filepath.Join(bobHome, ".gemini")
	if err := os.MkdirAll(gemDir, 0o700); err != nil {
		t.Fatal(err)
	}
	jetskiPath := filepath.Join(gemDir, "jetski-standalone-oauth-token")
	if err := os.WriteFile(jetskiPath, []byte(`{"writer_id":"w-keep-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := writeFallbackFileToDir(gemDir, Account{
		Email: "bob@example.com",
		Token: Token{AccessToken: "a1", RefreshToken: "r1", Expiry: time.Now().Unix() + 3600},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(jetskiPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc["writer_id"] != "w-keep-1" {
		t.Fatalf("writer_id not preserved: %s err=%v", raw, err)
	}
	email, tok, ok := readFallbackFileFromDir(gemDir)
	if !ok || email != "bob@example.com" || tok.RefreshToken != "r1" {
		t.Fatalf("readFallbackFileFromDir got email=%q tok=%+v ok=%v", email, tok, ok)
	}
	// 移除 wsl.conf 与 passwd 后，仍可通过 .gemini 目录标记定位到 bobHome。
	_ = os.Remove(filepath.Join(root, "etc", "wsl.conf"))
	_ = os.Remove(filepath.Join(root, "etc", "passwd"))
	if got := resolveWSLHomeFromRoot(root); got != bobHome {
		t.Fatalf("want .gemini fallback home %q, got %q", bobHome, got)
	}
}

func TestListLiveEmailOverrideAndFallback(t *testing.T) {
	s, err := LoadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a1, _ := s.upsert("one@example.com", "One", Token{RefreshToken: "r1", AccessToken: "a1"})
	a2, _ := s.upsert("two@example.com", "Two", Token{RefreshToken: "r2", AccessToken: "a2"})
	_ = s.setCurrent(a1.ID)

	m := NewManager(s, nil)
	live := "TWO@EXAMPLE.COM"
	m.liveEmailFn = func() string { return live }
	views := m.List()
	if views[0].Current || !views[1].Current || views[1].ID != a2.ID {
		t.Fatalf("expected live email to mark two@example.com current: %+v", views)
	}
	// 实机邮箱不在列表里时回退 CurrentID。
	live = "outside@example.com"
	m.invalidateLiveCache()
	views = m.List()
	if !views[0].Current || views[1].Current {
		t.Fatalf("expected fallback to CurrentID when live email not in store: %+v", views)
	}
}

func TestParseImportEntriesFormats(t *testing.T) {
	// Nested Antigravity-Manager export + BOM + duplicate refresh_token
	bom := []byte{0xEF, 0xBB, 0xBF}
	payload := append(bom, []byte(`{"accounts":[
		{"email":"one@example.com","custom_label":"Work","token":{"refresh_token":"r1"}},
		{"email":"dup@example.com","refresh_token":"r1"},
		{"email":"two@example.com","refresh_token":"r2"}
	]}`)...)
	entries, err := parseImportEntries(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "Work" || entries[0].RefreshToken != "r1" || entries[1].RefreshToken != "r2" {
		t.Fatalf("unexpected parsed entries: %+v", entries)
	}

	// Plain text 1// tokens
	rtA := "1//" + "aaa"
	rtB := "1//" + "bbb"
	entries, err = parseImportEntries([]byte(rtA + "\n" + rtB + ", " + rtA))
	if err != nil || len(entries) != 2 {
		t.Fatalf("plain text parse failed: entries=%+v err=%v", entries, err)
	}
}

func TestRefreshMarksInvalidGrantAndRefreshAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tok" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		switch r.Form.Get("refresh_token") {
		case "r-good":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-new", "expires_in": 3600})
		case "r-bad":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		default:
			http.Error(w, "bad", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	oldTokURL := googleTokenURL
	googleTokenURL = srv.URL + "/tok"
	t.Cleanup(func() { googleTokenURL = oldTokURL })

	s, err := LoadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	aGood, _ := s.upsert("good@example.com", "", Token{RefreshToken: "r-good", AccessToken: "old", Expiry: time.Now().Unix() + 100})
	aBad, _ := s.upsert("bad@example.com", "", Token{RefreshToken: "r-bad", AccessToken: "old", Expiry: time.Now().Unix() + 100})

	m := NewManager(s, nil)
	m.oauthClientOverride = &oauthClient{ID: "cid-1", Cred: "cred-1"}
	m.fetchQuotaFn = func(context.Context, *http.Client, string, *QuotaInfo) *QuotaInfo {
		return &QuotaInfo{Tier: "PRO", Buckets: []QuotaBucket{{Label: "Gemini", Percent: 85}}}
	}

	okN, failN, firstErr := m.RefreshAll(context.Background())
	if okN != 1 || failN != 1 || firstErr == nil {
		t.Fatalf("RefreshAll got ok=%d fail=%d err=%v", okN, failN, firstErr)
	}
	badAcc, _ := s.get(aBad.ID)
	if badAcc.Status != "invalid_grant" {
		t.Fatalf("expected invalid_grant status marked on %q, got %q", aBad.Email, badAcc.Status)
	}
	goodAcc, _ := s.get(aGood.ID)
	if goodAcc.Token.AccessToken != "at-new" || goodAcc.Quota == nil || goodAcc.Quota.Tier != "PRO" {
		t.Fatalf("unexpected good account state: %+v", goodAcc)
	}
}

func TestCandidateStateDBPaths(t *testing.T) {
	home := filepath.Join(t.TempDir(), "u")
	appdata := filepath.Join(home, "AppData", "Roaming")
	exe := filepath.Join(home, "Apps", "Antigravity.exe")
	paths := candidateStateDBPaths("windows", home, appdata, exe)
	if len(paths) != 3 || !strings.Contains(paths[0], "data") || !strings.Contains(paths[1], "Antigravity") || !strings.Contains(paths[2], "Antigravity IDE") {
		t.Fatalf("unexpected windows candidate paths: %v", paths)
	}
}
