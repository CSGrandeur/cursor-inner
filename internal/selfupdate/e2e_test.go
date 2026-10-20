package selfupdate

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 端到端：本机模拟 GitHub（固定清单地址）和 Gitee（API 取最新 tag），走完检查、验签、下载校验、预检、换文件、交接。

type e2e struct {
	t        *testing.T
	dir, exe string
	handover []string
	preflew  []string
}

func newE2E(t *testing.T) *e2e {
	dir := t.TempDir()
	exe := filepath.Join(dir, "cursor-inner.exe")
	if err := os.WriteFile(exe, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &e2e{t: t, dir: dir, exe: exe}
}

func (e *e2e) updater(current string, keys []TrustedKey, sources ...Source) *Updater {
	return New(Options{
		Version: current, Exe: e.exe, DataDir: e.dir, Sources: sources, Keys: keys,
		GOOS: "windows", GOARCH: "amd64", Timeout: 2 * time.Second,
		// 预检：模拟新版本的 --version，安装包内容就是它报告的版本号。
		Preflight: func(ctx context.Context, path, want string) error {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			e.preflew = append(e.preflew, string(b))
			if string(b) != want {
				return errors.New("新版本报告的版本号不对：" + string(b))
			}
			return nil
		},
		Handover: func(ctx context.Context, to string) error { e.handover = append(e.handover, to); return nil },
	})
}

func (e *e2e) exeContent() string {
	b, _ := os.ReadFile(e.exe)
	return string(b)
}

func (e *e2e) leftovers() []string {
	left, _ := filepath.Glob(filepath.Join(e.dir, "update", "*.part"))
	staged, _ := filepath.Glob(filepath.Join(e.dir, "update", "staged-*"))
	return append(left, staged...)
}

func githubSource(srv *httptest.Server) Source {
	return Source{Name: "github", URL: srv.URL + "/CSGrandeur/cursor-inner/releases/download/latest/manifest.json"}
}

func giteeSource(srv *httptest.Server) Source {
	return Source{Name: "gitee", LatestAPI: srv.URL + "/api/v5/repos/csgrandeur/cursor-inner/releases/latest", DownloadBase: srv.URL + "/csgrandeur/cursor-inner/releases/download/"}
}

const pkg = "cursor-inner-v0.5.0-windows-amd64.exe"

func TestE2ENormalUpgrade(t *testing.T) {
	k := newTestKey(t)
	_, gh := newFakeRelease(t, k, "v0.5.0", []byte("v0.5.0"))
	gt, gtSrv := newFakeRelease(t, k, "v0.5.0", []byte("v0.5.0"))
	gt.down = true
	e := newE2E(t)
	u := e.updater("v0.4.0", []TrustedKey{k.key}, githubSource(gh), giteeSource(gtSrv))
	st := u.Check(context.Background(), false)
	if st.State != StateAvailable || !st.Signed || st.KeyID != k.key.ID || st.Source != "github" || !st.CanApply || len(st.Sources) != 2 {
		t.Fatalf("%+v", st)
	}
	if st.Sources[1].Error == "" || st.Sources[0].Version != "v0.5.0" || !st.Sources[0].Chosen {
		t.Fatalf("sources %+v", st.Sources)
	}
	if err := u.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.exeContent() != "v0.5.0" || len(e.handover) != 1 || e.handover[0] != "v0.5.0" || u.Status().Mirror != "github" {
		t.Fatalf("exe=%q handover=%v %+v", e.exeContent(), e.handover, u.Status())
	}
	if loadTrust(e.dir).Highest != "v0.5.0" || len(e.leftovers()) != 0 {
		t.Fatalf("trust %+v left %v", loadTrust(e.dir), e.leftovers())
	}
}

func TestE2ETamperedPackage(t *testing.T) {
	k := newTestKey(t)
	gh, ghSrv := newFakeRelease(t, k, "v0.5.0", []byte("v0.5.0"))
	gt, gtSrv := newFakeRelease(t, k, "v0.5.0", []byte("v0.5.0"))
	gt.giteeTag = "v0.5.0"
	e := newE2E(t)
	u := e.updater("v0.4.0", []TrustedKey{k.key}, githubSource(ghSrv), giteeSource(gtSrv))
	st := u.Check(context.Background(), false)
	if st.State != StateAvailable {
		t.Fatalf("%+v", st)
	}
	// 一个镜像的包被篡改：丢掉它，换另一个镜像。两个源版本相同时选响应快的那个，
	// 所以篡改检查时选中的那个（否则选中的就是好的，测不到换镜像）。
	bad, good, other := gh, gt, "gitee"
	if st.Source == "gitee" {
		bad, good, other = gt, gh, "github"
	}
	bad.files[pkg] = []byte("v0.5.X") // 同样长度、内容被改
	if err := u.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.exeContent() != "v0.5.0" || bad.hit(pkg) == 0 || good.hit(pkg) == 0 {
		t.Fatalf("exe=%q hits bad=%d good=%d", e.exeContent(), bad.hit(pkg), good.hit(pkg))
	}
	if m := u.Status().Mirror; m != other {
		t.Fatalf("mirror %s, want %s", m, other)
	}
	gh.files[pkg] = []byte("v0.5.X")

	// 两边的包都被篡改：不换文件、不交接、不留文件，报校验失败。
	gt.files[pkg] = []byte("evil!!")
	e2 := newE2E(t)
	u2 := e2.updater("v0.4.0", []TrustedKey{k.key}, githubSource(ghSrv), giteeSource(gtSrv))
	u2.Check(context.Background(), false)
	err := u2.Apply(context.Background())
	if !errors.Is(err, ErrChecksum) || u2.Status().ErrorCode != CodeChecksum {
		t.Fatalf("err=%v %+v", err, u2.Status())
	}
	if e2.exeContent() != "old build" || len(e2.handover) != 0 || len(e2.preflew) != 0 || len(e2.leftovers()) != 0 {
		t.Fatalf("exe=%q handover=%v pre=%v left=%v", e2.exeContent(), e2.handover, e2.preflew, e2.leftovers())
	}
}

func TestE2ETamperedManifest(t *testing.T) {
	k := newTestKey(t)
	gh, ghSrv := newFakeRelease(t, k, "v0.5.0", []byte("v0.5.0"))
	// 签名之后改了清单：把包的 sha256 换成攻击者的文件。
	evil := []byte("evil!!")
	gh.manifest = manifestFor("v0.5.0", evil, "")
	gh.files[pkg] = evil
	e := newE2E(t)
	u := e.updater("v0.4.0", []TrustedKey{k.key}, githubSource(ghSrv))
	st := u.Check(context.Background(), false)
	if st.State != StateFailed || st.ErrorCode != CodeBadSignature || gh.hit(pkg) != 0 {
		t.Fatalf("%+v hits=%d", st, gh.hit(pkg))
	}
	if err := u.Apply(context.Background()); err == nil || e.exeContent() != "old build" {
		t.Fatalf("apply after a bad manifest: %v", err)
	}
	// Gitee 那边是好的：用 Gitee，并标出 GitHub 验签失败。
	gt, gtSrv := newFakeRelease(t, k, "v0.5.0", []byte("v0.5.0"))
	gt.giteeTag = "v0.5.0"
	u = e.updater("v0.4.0", []TrustedKey{k.key}, githubSource(ghSrv), giteeSource(gtSrv))
	st = u.Check(context.Background(), false)
	if st.State != StateAvailable || st.Source != "gitee" || st.Sources[0].Code != CodeBadSignature {
		t.Fatalf("%+v", st)
	}
	if err := u.Apply(context.Background()); err != nil || e.exeContent() != "v0.5.0" || gh.hit(pkg) != 0 {
		t.Fatalf("err=%v exe=%q gh hits=%d", err, e.exeContent(), gh.hit(pkg))
	}
}

func TestE2EWrongKeyAndMissingSignature(t *testing.T) {
	k, attacker := newTestKey(t), newTestKey(t)
	gh, ghSrv := newFakeRelease(t, attacker, "v0.5.0", []byte("v0.5.0"))
	e := newE2E(t)
	u := e.updater("v0.4.0", []TrustedKey{k.key}, githubSource(ghSrv))
	st := u.Check(context.Background(), false)
	if st.State != StateFailed || st.ErrorCode != CodeUnknownKey || !strings.Contains(st.Error, attacker.key.ID) {
		t.Fatalf("%+v", st)
	}
	gh.sig = nil
	st = u.Check(context.Background(), false)
	if st.State != StateFailed || st.ErrorCode != CodeNoSignature || gh.hit(pkg) != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestE2EDowngradeAndRollback(t *testing.T) {
	k := newTestKey(t)
	gh, ghSrv := newFakeRelease(t, k, "v0.4.0", []byte("v0.4.0"))
	e := newE2E(t)
	// 清单比当前旧：不更新。
	if st := e.updater("v0.5.0", []TrustedKey{k.key}, githubSource(ghSrv)).Check(context.Background(), false); st.State != StateUpToDate {
		t.Fatalf("%+v", st)
	}
	// 用户在 v0.4.0，见过 v0.6.0 但还没装；之后源改回 v0.5.0 的旧签名清单：识别为回滚。
	setRelease(gh, k, "v0.6.0")
	if st := e.updater("v0.4.0", []TrustedKey{k.key}, githubSource(ghSrv)).Check(context.Background(), false); st.State != StateAvailable || st.Latest != "v0.6.0" {
		t.Fatalf("%+v", st)
	}
	setRelease(gh, k, "v0.5.0")
	u := e.updater("v0.4.0", []TrustedKey{k.key}, githubSource(ghSrv))
	st := u.Check(context.Background(), false)
	if st.State != StateFailed || st.ErrorCode != CodeRollback || !strings.Contains(st.Error, "v0.6.0") {
		t.Fatalf("%+v", st)
	}
	if err := u.Apply(context.Background()); err == nil {
		t.Fatal("applied a rolled-back manifest")
	}
	// 另一个源给出 v0.6.0：用它。
	gt, gtSrv := newFakeRelease(t, k, "v0.6.0", []byte("v0.6.0"))
	gt.giteeTag = "v0.6.0"
	st = e.updater("v0.4.0", []TrustedKey{k.key}, githubSource(ghSrv), giteeSource(gtSrv)).Check(context.Background(), false)
	if st.State != StateAvailable || st.Latest != "v0.6.0" || st.Source != "gitee" {
		t.Fatalf("%+v", st)
	}
	// 装上 v0.6.0 以后，源给 v0.5.0 只是“已是最新”，不报回滚。
	if st := e.updater("v0.6.0", []TrustedKey{k.key}, githubSource(ghSrv)).Check(context.Background(), false); st.State != StateUpToDate {
		t.Fatalf("%+v", st)
	}
	// 签名有效但安装包报告的版本和清单不符（例如拿旧版本的包冒充）：预检拒绝，不交接。
	setRelease(gh, k, "v0.7.0")
	gh.files["cursor-inner-v0.7.0-windows-amd64.exe"] = []byte("v0.6.9")
	gh.manifest = manifestFor("v0.7.0", []byte("v0.6.9"), "")
	gh.sig = k.sign(gh.manifest)
	u = e.updater("v0.6.0", []TrustedKey{k.key}, githubSource(ghSrv))
	u.Check(context.Background(), false)
	if err := u.Apply(context.Background()); err == nil || len(e.handover) != 0 || e.exeContent() != "old build" {
		t.Fatalf("err=%v handover=%v", err, e.handover)
	}
}

func setRelease(r *fakeRelease, k testKey, version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.manifest = manifestFor(version, []byte(version), "")
	r.sig = k.sign(r.manifest)
	r.files["cursor-inner-"+version+"-windows-amd64.exe"] = []byte(version)
}

func TestE2EExpiredManifest(t *testing.T) {
	k := newTestKey(t)
	gh, ghSrv := newFakeRelease(t, k, "v0.5.0", []byte("v0.5.0"))
	gh.manifest = manifestFor("v0.5.0", []byte("v0.5.0"), `,"expires":"2020-01-01T00:00:00Z"`)
	gh.sig = k.sign(gh.manifest)
	st := newE2E(t).updater("v0.4.0", []TrustedKey{k.key}, githubSource(ghSrv)).Check(context.Background(), false)
	if st.State != StateFailed || st.ErrorCode != CodeExpired {
		t.Fatalf("%+v", st)
	}
}

func TestE2EMirrorSelection(t *testing.T) {
	k := newTestKey(t)
	gh, ghSrv := newFakeRelease(t, k, "v0.5.0", []byte("v0.5.0"))
	gt, gtSrv := newFakeRelease(t, k, "v0.5.0", []byte("v0.5.0"))
	gt.giteeTag = "v0.5.0"

	// GitHub 不通：用 Gitee，下载也从 Gitee。
	gh.down = true
	e := newE2E(t)
	u := e.updater("v0.4.0", []TrustedKey{k.key}, githubSource(ghSrv), giteeSource(gtSrv))
	if st := u.Check(context.Background(), false); st.State != StateAvailable || st.Source != "gitee" {
		t.Fatalf("%+v", st)
	}
	if err := u.Apply(context.Background()); err != nil || u.Status().Mirror != "gitee" || gt.hit(pkg) != 1 {
		t.Fatalf("err=%v %+v", err, u.Status())
	}

	// 两边同版本：用快的那个（GitHub 慢 300ms）；快的那个没有安装包时换另一个下载。
	gh.down = false
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond); gh.serve(w, r) }))
	defer slow.Close()
	delete(gt.files, pkg)
	e = newE2E(t)
	u = e.updater("v0.4.0", []TrustedKey{k.key}, githubSource(slow), giteeSource(gtSrv))
	if st := u.Check(context.Background(), false); st.Source != "gitee" {
		t.Fatalf("faster source not chosen: %+v", st)
	}
	if err := u.Apply(context.Background()); err != nil || u.Status().Mirror != "github" {
		t.Fatalf("err=%v %+v", err, u.Status())
	}

	// Gitee 比 GitHub 新：用 Gitee 的版本，即使它慢。
	setRelease(gt, k, "v0.6.0")
	gt.giteeTag = "v0.6.0"
	if st := e.updater("v0.4.0", []TrustedKey{k.key}, githubSource(ghSrv), giteeSource(gtSrv)).Check(context.Background(), false); st.Latest != "v0.6.0" || st.Source != "gitee" {
		t.Fatalf("%+v", st)
	}

	// Gitee API 给出的 tag 不是版本号（路径穿越）：这个源作废，不去拼地址。
	gt.giteeTag = "../../evil"
	st := newE2E(t).updater("v0.4.0", []TrustedKey{k.key}, githubSource(ghSrv), giteeSource(gtSrv)).Check(context.Background(), false)
	if st.Source != "github" || st.Sources[1].Code != CodeBadManifest {
		t.Fatalf("%+v", st)
	}
}

// 默认网络连不上时改走配置的出站代理，API、清单、签名、安装包都走同一条路径。
func TestE2EProxyRoute(t *testing.T) {
	k := newTestKey(t)
	gt, gtSrv := newFakeRelease(t, k, "v0.5.0", []byte("v0.5.0"))
	gt.giteeTag = "v0.5.0"
	target := strings.TrimPrefix(gtSrv.URL, "http://")
	src := Source{Name: "gitee", LatestAPI: "http://gitee.invalid/api/v5/repos/o/r/releases/latest", DownloadBase: "http://gitee.invalid/o/r/releases/download/"}
	e := newE2E(t)
	u := e.updater("v0.4.0", []TrustedKey{k.key}, src)
	viaProxy := 0
	u.opt.Proxy = func() (DialFunc, bool) {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			viaProxy++
			return (&net.Dialer{}).DialContext(ctx, network, target)
		}, true
	}
	st := u.Check(context.Background(), false)
	if st.State != StateAvailable || st.Route != "proxy" {
		t.Fatalf("%+v", st)
	}
	// 默认网络解析不了 gitee.invalid，下载成功本身就说明走的是代理路径。
	if err := u.Apply(context.Background()); err != nil || viaProxy == 0 || gt.hit(pkg) != 1 || u.Status().Mirror != "gitee" {
		t.Fatalf("err=%v proxy dials %d %+v", err, viaProxy, u.Status())
	}
}

// 没有内置公钥的构建：可以提示新版本，但拒绝安装，一个字节都不下载。
func TestE2EBuildWithoutKey(t *testing.T) {
	k := newTestKey(t)
	gh, ghSrv := newFakeRelease(t, k, "v0.5.0", []byte("v0.5.0"))
	e := newE2E(t)
	u := e.updater("v0.4.0", nil, githubSource(ghSrv))
	st := u.Check(context.Background(), false)
	if st.State != StateAvailable || st.CanApply || st.Signed || st.ErrorCode != CodeNoKey {
		t.Fatalf("%+v", st)
	}
	if err := u.Apply(context.Background()); !errors.Is(err, ErrNoKey) || gh.hit(pkg) != 0 || gh.hit("manifest.json.sig") != 0 {
		t.Fatalf("err=%v hits=%d", err, gh.hit(pkg))
	}
}
