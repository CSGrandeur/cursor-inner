package cursoraccount

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func makeTestJWT(sub, email string, exp int64) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payBytes, _ := json.Marshal(map[string]any{
		"sub":   sub,
		"email": email,
		"exp":   exp,
	})
	pay := base64.RawURLEncoding.EncodeToString(payBytes)
	sig := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return hdr + "." + pay + "." + sig
}

func TestStoreUpsertMergesAndProtectsMode(t *testing.T) {
	dir := t.TempDir()
	// 验证 BOM 容错读取。
	bomPath := filepath.Join(dir, "cursor-accounts.json")
	if err := os.WriteFile(bomPath, append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"accounts":[]}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a1, err := s.upsert("User@Example.com", "Alex", "auth0|1", "Auth_0", Token{AccessToken: "a1", RefreshToken: "r1", Expiry: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.saveMetaQuota(a1.ID, "", "", "", "", Token{}, &QuotaInfo{
		Tier:    "PRO+",
		Buckets: []QuotaBucket{{Label: "Plan", Percent: 80}},
	}, ""); err != nil {
		t.Fatal(err)
	}
	a2, err := s.upsert(" user@example.com ", "", "auth0|1", "", Token{AccessToken: "a2", RefreshToken: "r2", Expiry: 200})
	if err != nil {
		t.Fatal(err)
	}
	if a1.ID != a2.ID || a2.Name != "Alex" || a2.Token.RefreshToken != "r2" {
		t.Fatalf("merge mismatch: %+v", a2)
	}
	if a2.Quota == nil || a2.Quota.Tier != "PRO+" {
		t.Fatalf("quota should persist on re-upsert: %+v", a2.Quota)
	}
	info, err := os.Stat(bomPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestNormalizePastedTokenAndJWTClaims(t *testing.T) {
	jwt := makeTestJWT("github|u1", "Dev@Example.com", 1_900_000_000)
	cases := []string{
		jwt,
		"WorkosCursorSessionToken=user_01AB%3A%3A" + jwt + "; Path=/; HttpOnly",
		`"user_01AB::` + jwt + `"`,
	}
	for _, in := range cases {
		got := normalizePastedToken(in)
		if got != jwt {
			t.Fatalf("normalize(%q) = %q, want %q", in, got, jwt)
		}
	}
	sub, email, exp := parseJWTClaims(jwt)
	if sub != "github|u1" || email != "dev@example.com" || exp != 1_900_000_000 {
		t.Fatalf("claims mismatch: sub=%q email=%q exp=%d", sub, email, exp)
	}
}

func TestSafeStorageCryptoHelpers(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	jwt := makeTestJWT("sub1", "enc@example.com", 1_900_000_000)
	enc, err := encryptAES256GCM(key, jwt)
	if err != nil {
		t.Fatal(err)
	}
	rawBlob, err := base64.StdEncoding.DecodeString(enc[len(safeStoragePrefix):])
	if err != nil {
		t.Fatal(err)
	}
	dec, err := decryptAES256GCM(key, rawBlob)
	if err != nil || dec != jwt {
		t.Fatalf("GCM roundtrip failed: dec=%q err=%v", dec, err)
	}

	// 验证 Linux / macOS v10 AES-128-CBC 解密路径。
	derived, err := pbkdf2.Key(sha1.New, "peanuts", []byte("saltysalt"), 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(derived)
	plain := []byte(jwt)
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	ct := make([]byte, len(padded))
	cipher.NewCBCDecrypter(block, []byte("                ")) // 只检长度对齐
	encorer := cipher.NewCBCEncrypter(block, []byte("                "))
	encorer.CryptBlocks(ct, padded)
	v10 := append([]byte("v10"), ct...)
	gotCBC, err := decryptAES128CBC("peanuts", 1, v10)
	if err != nil || gotCBC != jwt {
		t.Fatalf("CBC decrypt failed: got=%q err=%v", gotCBC, err)
	}
	// 无前缀时向后兼容明文读取。
	plainOut, err := decodeStoredToken("", jwt)
	if err != nil || plainOut != jwt {
		t.Fatalf("plaintext passthrough mismatch: %q err=%v", plainOut, err)
	}
}

func TestInjectAccountBackupAndStaleCleanup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "Cursor", "User", "globalStorage", "state.vscdb")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?), (?, ?), (?, ?), (?, ?), (?, ?)`,
		"filler.keep", "yes",
		keyCachedTeam, `{"id":99}`,
		keyTeamID, "99",
		keyStripeCustomer, "cus_old",
		keyXAISession, `{"secret":"old"}`,
	); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	jwt := makeTestJWT("github|new", "new@example.com", 2_000_000_000)
	a := Account{
		Email:      "New@Example.com",
		Name:       "New User",
		AuthID:     "github|new",
		SignUpType: "Github",
		Token:      Token{AccessToken: jwt, RefreshToken: jwt, Expiry: 2_000_000_000},
		Quota:      &QuotaInfo{Tier: "ULTRA"},
	}
	if err := injectAccount(dbPath, a); err != nil {
		t.Fatal(err)
	}

	bak, err := sql.Open("sqlite", dbPath+".cursor-inner-bak")
	if err != nil {
		t.Fatal(err)
	}
	var oldTeam string
	if err := bak.QueryRow(`SELECT value FROM ItemTable WHERE key=?`, keyTeamID).Scan(&oldTeam); err != nil || oldTeam != "99" {
		_ = bak.Close()
		t.Fatalf("backup lost old key: %v %q", err, oldTeam)
	}
	_ = bak.Close()

	if live := LiveEmailFromDB(dbPath); live != "new@example.com" {
		t.Fatalf("LiveEmailFromDB=%q", live)
	}
	got, ok, err := CurrentFromState(dbPath)
	if err != nil || !ok {
		t.Fatalf("CurrentFromState ok=%v err=%v", ok, err)
	}
	if got.Email != "new@example.com" || got.Name != "New User" || got.AuthID != "github|new" || got.Quota == nil || got.Quota.Tier != "ULTRA" {
		t.Fatalf("read back mismatch: %+v", got)
	}
	// 确认跨账号残留键已经被清掉。
	check, _ := openStateDB(dbPath, true)
	defer check.Close()
	for _, stale := range []string{keyCachedTeam, keyTeamID, keyStripeCustomer, keyXAISession} {
		if v := readItemText(check, stale); v != "" {
			t.Fatalf("stale key %s still present: %q", stale, v)
		}
	}
	if v := readItemText(check, "filler.keep"); v != "yes" {
		t.Fatalf("unrelated key damaged: %q", v)
	}
}

func TestLoginRefreshQuotaImportAndSwitch(t *testing.T) {
	oldWeb, oldBack := cursorWebsiteURL, cursorBackendURL
	oldSettle := postStopSettle
	postStopSettle = 0
	t.Cleanup(func() {
		cursorWebsiteURL = oldWeb
		cursorBackendURL = oldBack
		postStopSettle = oldSettle
	})

	exp := time.Now().Add(2 * time.Hour).Unix()
	jwt1 := makeTestJWT("auth0|one", "one@example.com", exp)
	jwt2 := makeTestJWT("github|two", "two@example.com", exp)

	var (
		mu           sync.Mutex
		pollCount    int
		shouldLogout bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/auth/poll":
			pollCount++
			if pollCount == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accessToken":  jwt1,
				"refreshToken": jwt1,
				"authId":       "auth0|one",
			})
		case "/oauth/token":
			var req struct {
				RefreshToken string `json:"refresh_token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if shouldLogout {
				_ = json.NewEncoder(w).Encode(map[string]any{"shouldLogout": true, "error": "revoked"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": req.RefreshToken})
		case "/aiserver.v1.AuthService/GetEmail":
			if r.Header.Get("Authorization") == "Bearer "+jwt2 {
				_ = json.NewEncoder(w).Encode(map[string]any{"email": "two@example.com", "signUpType": "SIGN_UP_TYPE_GITHUB"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "one@example.com", "signUpType": "SIGN_UP_TYPE_AUTH_0"})
		case "/aiserver.v1.DashboardService/GetMe":
			_ = json.NewEncoder(w).Encode(map[string]any{"authId": "auth0|one", "firstName": "One", "lastName": "User"})
		case "/auth/full_stripe_profile":
			if r.Header.Get("Authorization") == "Bearer "+jwt2 {
				_ = json.NewEncoder(w).Encode(map[string]any{"membershipType": "ultra"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"membershipType": "pro_plus"})
		case "/aiserver.v1.DashboardService/GetCurrentPeriodUsage":
			resetMS := time.Now().Add(72 * time.Hour).UnixMilli()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"billingCycleEnd": json.Number(string(strconvItoa64(resetMS))),
				"enabled":         true,
				"planUsage": map[string]any{
					"includedSpend": 500,
					"limit":         2000,
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	cursorWebsiteURL = srv.URL
	cursorBackendURL = srv.URL

	s, err := LoadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(s, nil)
	var openedURL string
	var stopped, started, running atomic.Bool
	m.SetHooks(
		func() error { stopped.Store(true); running.Store(false); return nil },
		func() error { started.Store(true); running.Store(true); return nil },
		func() bool { return running.Load() },
		func(u string) error { openedURL = u; return nil },
	)

	// 1. PKCE 登录并拿齐 tier + Plan 剩余配额（2000 - 500 -> 75%）
	authURL, err := m.LoginStart()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authURL)
	if err != nil || u.Query().Get("challenge") == "" || u.Query().Get("uuid") == "" || openedURL == "" {
		t.Fatalf("unexpected authURL=%q opened=%q err=%v", authURL, openedURL, err)
	}
	v1, err := m.LoginWait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v1.Email != "one@example.com" || v1.Tier != "PRO+" || len(v1.Quotas) != 1 || v1.Quotas[0].Percent != 75 || v1.Quotas[0].ResetAt == 0 {
		t.Fatalf("unexpected view after login: %+v", v1)
	}

	// 2. 批量导入去重
	batch := "WorkosCursorSessionToken=user_1%3A%3A" + jwt2 + "\n" + jwt2 + "\n"
	rs, err := m.ImportJSON(context.Background(), []byte(batch))
	if err != nil || len(rs) != 1 || !rs[0].OK || rs[0].Email != "two@example.com" {
		t.Fatalf("ImportJSON got %+v err=%v", rs, err)
	}

	// 3. Switch 成功路径（注入 mock creds、停并重启）
	var injected Account
	m.applyCreds = func(a Account) error { injected = a; return nil }
	running.Store(true)
	var id2 string
	for _, v := range m.List() {
		if v.Email == "two@example.com" {
			id2 = v.ID
		}
	}
	if err := m.Switch(context.Background(), id2); err != nil {
		t.Fatal(err)
	}
	if !stopped.Load() || !started.Load() || injected.Email != "two@example.com" {
		t.Fatalf("switch hooks mismatch: stop=%v start=%v injected=%+v", stopped.Load(), started.Load(), injected)
	}

	// 4. Refresh shouldLogout -> 标记 expired 且 Switch 在停进程前中止
	mu.Lock()
	shouldLogout = true
	mu.Unlock()
	if _, err := m.Refresh(context.Background(), id2); err == nil {
		t.Fatal("expected expired error on shouldLogout")
	}
	stopped.Store(false)
	started.Store(false)
	for _, v := range m.List() {
		if v.ID == id2 && v.Status != "expired" {
			t.Fatalf("expected status=expired, got %+v", v)
		}
	}
}

func strconvItoa64(v int64) []byte {
	return json.RawMessage([]byte(
		func() string {
			b, _ := json.Marshal(v)
			return string(b)
		}(),
	))
}
