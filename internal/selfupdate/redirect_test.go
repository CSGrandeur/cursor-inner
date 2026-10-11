package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// 真实 GitHub 的跳转：releases/latest/download/X → 同主机 releases/download/vX.Y.Z/X →
// 另一台主机上带签名参数的 CDN 地址（release-assets.githubusercontent.com/...?sig=...）。
// CDN 只认自己的 blob 编号、无视多出来的参数，所以在 CDN 地址后面拼 ".sig" 拿回来的是清单本身，
// 按 CDN 目录拼安装包名是 404。签名和安装包必须按 vX.Y.Z 那一跳的目录去拼。
func newGitHubLike(t *testing.T, r *fakeRelease, version string) *httptest.Server {
	t.Helper()
	cdn := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(cdn.Close)
	var mu sync.Mutex
	blob := map[string]string{} // blob 编号 → 文件名
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		const latest = "/CSGrandeur/cursor-inner/releases/latest/download/"
		const tagged = "/CSGrandeur/cursor-inner/releases/download/"
		switch {
		case strings.HasPrefix(req.URL.Path, latest):
			http.Redirect(w, req, tagged+version+"/"+strings.TrimPrefix(req.URL.Path, latest), http.StatusFound)
		case strings.HasPrefix(req.URL.Path, tagged+version+"/"):
			name := strings.TrimPrefix(req.URL.Path, tagged+version+"/")
			id := "c55bb884-" + sha([]byte(name))[:8]
			mu.Lock()
			blob[id] = name
			mu.Unlock()
			http.Redirect(w, req, cdn.URL+"/github-production-release-asset/1404137106/"+id+"?sp=r&sig=abc&rsct=application%2Foctet-stream", http.StatusFound)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(gh.Close)
	cdn.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		id := strings.TrimPrefix(req.URL.Path, "/github-production-release-asset/1404137106/")
		mu.Lock()
		name, ok := blob[id]
		mu.Unlock()
		if !ok {
			http.Error(w, "BlobNotFound", http.StatusNotFound)
			return
		}
		req2 := req.Clone(req.Context())
		req2.URL.Path = "/" + name
		r.serve(w, req2)
	})
	return gh
}

func TestE2EGitHubRedirectToCDN(t *testing.T) {
	k := newTestKey(t)
	r, _ := newFakeRelease(t, k, "v0.5.0", []byte("v0.5.0"))
	gh := newGitHubLike(t, r, "v0.5.0")
	e := newE2E(t)
	src := Source{Name: "github", URL: gh.URL + "/CSGrandeur/cursor-inner/releases/latest/download/manifest.json"}
	u := e.updater("v0.4.0", []TrustedKey{k.key}, src)
	st := u.Check(context.Background(), false)
	if st.State != StateAvailable || !st.Signed || st.Latest != "v0.5.0" {
		t.Fatalf("check through the CDN redirect: %+v", st)
	}
	if err := u.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.exeContent() != "v0.5.0" || r.hit("manifest.json.sig") != 1 || r.hit(pkg) != 1 {
		t.Fatalf("exe=%q sig hits %d pkg hits %d", e.exeContent(), r.hit("manifest.json.sig"), r.hit(pkg))
	}
}

func TestBaseURLAndSiblings(t *testing.T) {
	base := "https://github.com/o/r/releases/download/v1.2.3/manifest.json"
	if got := sigURL(base); got != base+".sig" {
		t.Fatal(got)
	}
	if got := sigURL("https://h/x/manifest.json?token=1"); got != "https://h/x/manifest.json.sig" {
		t.Fatal(got)
	}
	a := Asset{Name: "cursor-inner-v1.2.3-windows-amd64.exe", URLs: []string{"https://github.com/o/r/releases/download/v1.2.3/cursor-inner-v1.2.3-windows-amd64.exe", "https://mirror/x.exe"}}
	got := assetURLs(a, base)
	if len(got) != 2 || got[0] != a.URLs[0] || got[1] != "https://mirror/x.exe" {
		t.Fatalf("dedup, own directory first: %v", got)
	}
	got = assetURLs(a, "https://mirror.example/o/r/releases/download/v1.2.3/manifest.json")
	if len(got) != 3 || !strings.HasPrefix(got[0], "https://mirror.example/") {
		t.Fatalf("each source downloads from its own directory first: %v", got)
	}
}
