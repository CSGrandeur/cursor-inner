package antiaccount

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 金样校验：动态构造与 Antigravity state.vscdb 同构的小库/跨页大库，
// 注入账号、替换大值逼出页重排，再读回核对。
func createSampleStateDB(t *testing.T, path string, fillerCount int, bigFiller bool) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE, value BLOB)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?), (?, ?)`,
		keyOnboarding, []byte("true"),
		keyLegacyInit, []byte("legacy"),
	); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	for i := 0; i < fillerCount; i++ {
		val := []byte(fmt.Sprintf("filler-payload-%04d", i))
		if bigFiller && i%10 == 0 {
			val = []byte(strings.Repeat("x", 4096))
		}
		if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?)`, fmt.Sprintf("filler.key.%04d", i), val); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGoldenFixtureRoundTrip(t *testing.T) {
	cases := []struct {
		name        string
		fillerCount int
		bigFiller   bool
	}{
		{name: "small", fillerCount: 5},
		{name: "big", fillerCount: 80, bigFiller: true},
	}
	for _, tc := range cases {
		name := tc.name
		dst := filepath.Join(t.TempDir(), name+".vscdb")
		createSampleStateDB(t, dst, tc.fillerCount, tc.bigFiller)
		db, err := readStateDB(dst)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if v, ok := db.Get("antigravityOnboarding"); !ok || string(v) != "true" {
			_ = db.Close()
			t.Fatalf("%s: onboarding=%q ok=%v", name, v, ok)
		}
		if v, ok := db.Get("filler.key.0003"); !ok || len(v) == 0 {
			_ = db.Close()
			t.Fatalf("%s: filler read failed", name)
		}
		_ = db.Close()
		a := Account{
			Email: "fixture@example.com",
			Name:  "Fixture",
			Token: Token{AccessToken: "a-" + name, RefreshToken: "r-" + name, IDToken: "i-" + name, Expiry: time.Now().Unix() + 3600},
		}
		if err := injectAccount(dst, a); err != nil {
			t.Fatalf("%s inject: %v", name, err)
		}
		// 保持 os 导入用于清理/校验文件存在性
		if _, err := os.Stat(dst); err != nil {
			t.Fatalf("%s stat: %v", name, err)
		}
		email, gotName, token, ok, err := CurrentFromDB(dst)
		if err != nil || !ok {
			t.Fatalf("%s re-read: ok=%v err=%v", name, ok, err)
		}
		if email != a.Email || gotName != a.Name || token.AccessToken != a.Token.AccessToken || token.RefreshToken != a.Token.RefreshToken || token.IDToken != a.Token.IDToken {
			t.Fatalf("%s re-read mismatch: %+v", name, token)
		}
		db2, err := readStateDB(dst)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := db2.Get("jetskiStateSync.agentManagerInitState"); ok {
			_ = db2.Close()
			t.Fatalf("%s legacy key still present", name)
		}
		if v, ok := db2.Get("filler.key.0003"); !ok || len(v) == 0 {
			_ = db2.Close()
			t.Fatalf("%s filler lost after write", name)
		}
		// 大值替换：约 6KB payload，超过单 cell 上限，逼出整页重排。
		big := make([]byte, 6000)
		for i := range big {
			big[i] = byte('a' + i%26)
		}
		bigTopic := topicWithEntry(nil, oauthSentinelKey, big)
		if err := db2.Put(keyUnifiedOAuth, []byte(base64.StdEncoding.EncodeToString(bigTopic))); err != nil {
			_ = db2.Close()
			t.Fatalf("%s big put: %v", name, err)
		}
		if err := writeStateDB(dst, db2); err != nil {
			t.Fatal(err)
		}
		db3, err := readStateDB(dst)
		if err != nil {
			t.Fatal(err)
		}
		v, ok := db3.Get(keyUnifiedOAuth)
		_ = db3.Close()
		if !ok {
			t.Fatalf("%s big value lost", name)
		}
		topic, _ := base64.StdEncoding.DecodeString(string(v))
		entries := parseTopic(topic)
		if len(entries) != 1 || string(entries[0].payload) != string(big) {
			t.Fatalf("%s big value mismatch: %d entries", name, len(entries))
		}
	}
}
