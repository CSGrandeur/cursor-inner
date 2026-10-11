package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReleaseArtifacts 是发版流水线的出厂检查，平时跳过：
//
//	UPDATE_RELEASE_DIR=out UPDATE_RELEASE_VERSION=v0.5.0 go test ./internal/selfupdate -run TestReleaseArtifacts
//
// 用源码里的发版公钥（不含 extraKeys）验 out/manifest.json 的签名，确认版本号、product，
// 并逐个核对清单里每个安装包的大小和 sha256。任何一项不对，发版就停下，不会发出客户端会拒收的清单。
func TestReleaseArtifacts(t *testing.T) {
	dir := os.Getenv("UPDATE_RELEASE_DIR")
	if dir == "" {
		t.Skip("UPDATE_RELEASE_DIR not set")
	}
	keys, err := parseKeys(releaseKeys)
	if err != nil || len(keys) == 0 {
		t.Fatalf("release keys: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join(dir, "manifest.json.sig"))
	if err != nil {
		t.Fatalf("manifest.json.sig: %v (set the UPDATE_SIGNING_KEY secret)", err)
	}
	m, keyID, err := ParseManifest(raw, sig, keys, time.Now())
	if err != nil {
		t.Fatalf("the released manifest would be rejected by clients: %v", err)
	}
	t.Logf("manifest %s signed by key %s", m.Version, keyID)
	if want := os.Getenv("UPDATE_RELEASE_VERSION"); want != "" && m.Version != want {
		t.Fatalf("manifest version %s, tag %s", m.Version, want)
	}
	for _, p := range [][2]string{{"windows", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}} {
		if _, err := m.Pick(p[0], p[1]); err != nil {
			t.Errorf("no asset for %s/%s", p[0], p[1])
		}
	}
	for _, a := range m.Assets {
		f, err := os.Open(filepath.Join(dir, a.Name))
		if err != nil {
			t.Errorf("%s: %v", a.Name, err)
			continue
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil || n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
			t.Errorf("%s: size %d sha %x, manifest says %d %s", a.Name, n, h.Sum(nil), a.Size, a.SHA256)
		}
	}
}
