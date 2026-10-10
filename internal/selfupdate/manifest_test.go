package selfupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const goodManifest = `{"schema":1,"version":"v0.4.0","notes":"- 新功能","assets":[
 {"os":"windows","arch":"amd64","name":"cursor-inner-v0.4.0-windows-amd64.exe","size":3,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
 {"os":"linux","arch":"amd64","name":"cursor-inner-v0.4.0-linux-amd64","size":3,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad","urls":["https://mirror.example/x"]}]}`

func TestParseManifestUnsigned(t *testing.T) {
	m, signed, err := ParseManifest([]byte(goodManifest), nil, nil)
	if err != nil || signed {
		t.Fatalf("err=%v signed=%v", err, signed)
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
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	raw := []byte(goodManifest)
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw)))
	if _, signed, err := ParseManifest(raw, sig, pub); err != nil || !signed {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if _, _, err := ParseManifest(raw, nil, pub); !errors.Is(err, ErrNoSignature) {
		t.Fatalf("missing sig: %v", err)
	}
	tampered := []byte(strings.Replace(goodManifest, "v0.4.0\"", "v0.5.0\"", 1))
	if _, _, err := ParseManifest(tampered, sig, pub); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered accepted: %v", err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, _, err := ParseManifest(raw, sig, other); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong key accepted: %v", err)
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
	}
	for name, raw := range bad {
		if _, _, err := ParseManifest([]byte(raw), nil, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
