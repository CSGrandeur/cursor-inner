package selfupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const goodManifest = `{"schema":1,"product":"cursor-inner","version":"v0.4.0","notes":"- 新功能","assets":[
 {"os":"windows","arch":"amd64","name":"cursor-inner-v0.4.0-windows-amd64.exe","size":3,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
 {"os":"linux","arch":"amd64","name":"cursor-inner-v0.4.0-linux-amd64","size":3,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad","urls":["https://mirror.example/x"]}]}`

func TestParseManifestUnsigned(t *testing.T) {
	m, keyID, err := ParseManifest([]byte(goodManifest), nil, nil, time.Now())
	if err != nil || keyID != "" {
		t.Fatalf("err=%v key=%v", err, keyID)
	}
	a, err := m.Pick("windows", "amd64")
	if err != nil || a.Size != 3 {
		t.Fatalf("pick %v %v", a, err)
	}
	if _, err := m.Pick("darwin", "arm64"); !errors.Is(err, ErrNoAsset) {
		t.Fatalf("want ErrNoAsset, got %v", err)
	}
	urls := assetURLs(a, "https://github.com/o/r/releases/download/v0.4.0/manifest.json")
	if len(urls) != 1 || urls[0] != "https://github.com/o/r/releases/download/v0.4.0/cursor-inner-v0.4.0-windows-amd64.exe" {
		t.Fatalf("urls %v", urls)
	}
	l, _ := m.Pick("linux", "amd64")
	if u := assetURLs(l, "https://x/manifest.json"); u[0] != "https://mirror.example/x" {
		t.Fatalf("explicit urls ignored: %v", u)
	}
}

func TestParseManifestSignature(t *testing.T) {
	k := newTestKey(t)
	keys := []TrustedKey{k.key}
	raw := []byte(goodManifest)
	now := time.Now()
	if _, id, err := ParseManifest(raw, k.sign(raw), keys, now); err != nil || id != k.key.ID {
		t.Fatalf("valid signature rejected: %v %q", err, id)
	}
	// 旧格式：一段 base64，没有 key id。
	bare := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(k.priv, raw)))
	if _, id, err := ParseManifest(raw, bare, keys, now); err != nil || id != k.key.ID {
		t.Fatalf("bare signature rejected: %v", err)
	}
	if _, _, err := ParseManifest(raw, nil, keys, now); !errors.Is(err, ErrNoSignature) || ErrorCode(err) != CodeNoSignature {
		t.Fatalf("missing sig: %v", err)
	}
	tampered := []byte(strings.Replace(goodManifest, "v0.4.0\"", "v0.5.0\"", 1))
	if _, _, err := ParseManifest(tampered, k.sign(raw), keys, now); !errors.Is(err, ErrBadSignature) || ErrorCode(err) != CodeBadSignature {
		t.Fatalf("tampered accepted: %v", err)
	}
	other := newTestKey(t)
	_, _, err := ParseManifest(raw, other.sign(raw), keys, now)
	if !errors.Is(err, ErrUnknownKey) || !strings.Contains(err.Error(), other.key.ID) {
		t.Fatalf("wrong key: %v", err)
	}
	// 没有 key id 的签名用错钥签：只能说签名不对。
	if _, _, err := ParseManifest(raw, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(other.priv, raw))), keys, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("bare wrong key: %v", err)
	}
	// 冒用可信 key id、签名却是别的钥签的。
	forged, _ := json.Marshal(sigFile{Signatures: []sigEntry{{KeyID: k.key.ID, Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(other.priv, raw))}}})
	if _, _, err := ParseManifest(raw, forged, keys, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("forged key id: %v", err)
	}
	if _, _, err := ParseManifest(raw, []byte("{not json and not base64"), keys, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("garbage sig: %v", err)
	}
}

// 轮换：签名文件带新旧两条签名，只认其中一把的客户端也能验过；客户端同时认新旧两把也行。
func TestKeyRotation(t *testing.T) {
	oldK, newK := newTestKey(t), newTestKey(t)
	raw := []byte(goodManifest)
	both, _ := json.Marshal(sigFile{Signatures: []sigEntry{
		{KeyID: oldK.key.ID, Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(oldK.priv, raw))},
		{KeyID: newK.key.ID, Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(newK.priv, raw))},
	}})
	for name, keys := range map[string][]TrustedKey{"old only": {oldK.key}, "new only": {newK.key}, "both": {oldK.key, newK.key}} {
		if _, _, err := ParseManifest(raw, both, keys, time.Now()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, id, err := ParseManifest(raw, newK.sign(raw), []TrustedKey{oldK.key, newK.key}, time.Now()); err != nil || id != newK.key.ID {
		t.Fatalf("new key alone: %v %s", err, id)
	}
}

func TestManifestExpiryAndProduct(t *testing.T) {
	k := newTestKey(t)
	keys := []TrustedKey{k.key}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	exp := manifestFor("v0.5.0", []byte("abc"), `,"expires":"2026-10-01T00:00:00Z"`)
	if _, _, err := ParseManifest(exp, k.sign(exp), keys, now); !errors.Is(err, ErrExpired) || ErrorCode(err) != CodeExpired {
		t.Fatalf("expired accepted: %v", err)
	}
	fresh := manifestFor("v0.5.0", []byte("abc"), `,"expires":"2026-12-01T00:00:00Z"`)
	if _, _, err := ParseManifest(fresh, k.sign(fresh), keys, now); err != nil {
		t.Fatalf("fresh rejected: %v", err)
	}
	noProduct := []byte(strings.Replace(string(manifestFor("v0.5.0", []byte("abc"), "")), `"product":"cursor-inner",`, "", 1))
	if _, _, err := ParseManifest(noProduct, k.sign(noProduct), keys, now); !errors.Is(err, ErrNotProduct) {
		t.Fatalf("signed manifest without product: %v", err)
	}
	other := []byte(strings.Replace(string(manifestFor("v0.5.0", []byte("abc"), "")), `"cursor-inner"`, `"something-else"`, 1))
	if _, _, err := ParseManifest(other, k.sign(other), keys, now); !errors.Is(err, ErrNotProduct) {
		t.Fatalf("other product: %v", err)
	}
}

func TestTrustedKeys(t *testing.T) {
	keys, err := TrustedKeys()
	if err != nil || len(keys) == 0 {
		t.Fatalf("release keys must be compiled in: %v %v", keys, err)
	}
	if keys[0].ID != "eb68418a53240b13" {
		t.Fatalf("key id %s", keys[0].ID)
	}
	old := extraKeys
	t.Cleanup(func() { extraKeys = old })
	k := newTestKey(t)
	extraKeys = base64.StdEncoding.EncodeToString(k.key.Pub) + ", not-a-key"
	keys, err = TrustedKeys()
	if err == nil || len(keys) != 2 || keys[1].ID != k.key.ID {
		t.Fatalf("%v %v", keys, err)
	}
}

func TestParseManifestRejects(t *testing.T) {
	bad := map[string]string{
		"schema":  `{"schema":2,"version":"v1.0.0","assets":[]}`,
		"version": `{"schema":1,"version":"latest","assets":[]}`,
		"sha":     `{"schema":1,"version":"v1.0.0","assets":[{"os":"linux","arch":"amd64","name":"a","size":1,"sha256":"abc"}]}`,
		"path":    `{"schema":1,"version":"v1.0.0","assets":[{"os":"linux","arch":"amd64","name":"../a","size":1,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}]}`,
		"http":    `{"schema":1,"version":"v1.0.0","assets":[{"os":"linux","arch":"amd64","name":"a","size":1,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad","urls":["http://x/a"]}]}`,
		"size":    `{"schema":1,"version":"v1.0.0","assets":[{"os":"linux","arch":"amd64","name":"a","size":0,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}]}`,
		"json":    `{`,
		"dup":     `{"schema":1,"version":"v1.0.0","assets":[{"os":"linux","arch":"amd64","name":"a","size":1,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},{"os":"linux","arch":"amd64","name":"b","size":1,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}]}`,
		"expires": `{"schema":1,"version":"v1.0.0","expires":"soon","assets":[]}`,
	}
	for name, raw := range bad {
		if _, _, err := ParseManifest([]byte(raw), nil, nil, time.Now()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
