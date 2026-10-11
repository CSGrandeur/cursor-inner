package selfupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type testKey struct {
	priv ed25519.PrivateKey
	key  TrustedKey
}

func newTestKey(t *testing.T) testKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{priv: priv, key: TrustedKey{ID: KeyID(pub), Pub: pub}}
}

// sign 产出 manifest.json.sig（带 key id 的格式）。
func (k testKey) sign(raw []byte) []byte {
	b, _ := json.Marshal(sigFile{Signatures: []sigEntry{{KeyID: k.key.ID, Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(k.priv, raw))}}})
	return b
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// manifestFor 生成一份 windows/amd64 的清单，安装包内容是 payload。
func manifestFor(version string, payload []byte, extra string) []byte {
	return []byte(fmt.Sprintf(`{"schema":1,"product":"cursor-inner","version":%q,"notes":"- 说明"%s,"assets":[{"os":"windows","arch":"amd64","name":"cursor-inner-%s-windows-amd64.exe","size":%d,"sha256":%q}]}`,
		version, extra, version, len(payload), sha(payload)))
}

// fakeRelease 模拟一个 Release 目录：manifest.json、manifest.json.sig 和安装包。字段可以在测试里改来模拟篡改。
type fakeRelease struct {
	mu       sync.Mutex
	manifest []byte
	sig      []byte
	files    map[string][]byte
	hits     map[string]int
	down     bool
	// giteeTag 非空时还提供 Gitee 风格的「最新版本」API。
	giteeTag string
}

func newFakeRelease(t *testing.T, k testKey, version string, payload []byte) (*fakeRelease, *httptest.Server) {
	t.Helper()
	m := manifestFor(version, payload, "")
	r := &fakeRelease{manifest: m, sig: k.sign(m), files: map[string][]byte{"cursor-inner-" + version + "-windows-amd64.exe": payload}, hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(srv.Close)
	return r, srv
}

func (r *fakeRelease) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
	r.hits[name]++
	if r.down {
		http.Error(w, "down", http.StatusBadGateway)
		return
	}
	switch {
	case strings.HasSuffix(req.URL.Path, "/releases/latest") && r.giteeTag != "":
		fmt.Fprintf(w, `{"tag_name":%q}`, r.giteeTag)
	case name == "manifest.json":
		w.Write(r.manifest)
	case name == "manifest.json.sig":
		if r.sig == nil {
			http.NotFound(w, req)
			return
		}
		w.Write(r.sig)
	default:
		b, ok := r.files[name]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Write(b)
	}
}

func (r *fakeRelease) hit(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[name]
}
