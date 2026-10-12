package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"cursor-inner/internal/selfupdate"
	"cursor-inner/internal/updateview"
)

// fakeUpdate 是带自更新的后端：真的 Updater，更新源是一个总是 404 的本机服务器（检查 → 失败）。
type fakeUpdate struct {
	*fake
	u *selfupdate.Updater
}

func (f *fakeUpdate) UpdateStatus() (selfupdate.Status, error) { return f.u.Status(), nil }
func (f *fakeUpdate) CheckUpdate(auto bool) (selfupdate.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return f.u.Check(ctx, auto), nil
}
func (f *fakeUpdate) ApplyUpdate() error                      { return nil }
func (f *fakeUpdate) WatchUpdate() (*selfupdate.Watch, error) { return f.u.Watch(), nil }

func newFakeUpdate(t *testing.T) *fakeUpdate {
	src := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(src.Close)
	u := selfupdate.New(selfupdate.Options{Version: "v1.0.0", Sources: []selfupdate.Source{selfupdate.URLSource(src.URL + "/manifest.json")}, Timeout: 2 * time.Second})
	return &fakeUpdate{fake: &fake{}, u: u}
}

type sseEvent struct {
	id   string
	data selfupdate.Status
}

func readEvents(t *testing.T, sc *bufio.Scanner, n int) []sseEvent {
	t.Helper()
	var out []sseEvent
	var cur sseEvent
	for len(out) < n && sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &cur.data); err != nil {
				t.Fatal(err)
			}
		case line == "" && cur.id != "":
			out = append(out, cur)
			cur = sseEvent{}
		}
	}
	if len(out) < n {
		t.Fatalf("got %d events, want %d (err %v)", len(out), n, sc.Err())
	}
	return out
}

// 推送：先发当前状态，之后每个阶段一条；id 是快照序号，单调递增。CloseStreams 结束推送连接。
func TestUpdateEventsStream(t *testing.T) {
	b := newFakeUpdate(t)
	h := Handler(b)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/update/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers %v", resp.Header)
	}
	sc := bufio.NewScanner(resp.Body)
	first := readEvents(t, sc, 1)
	if first[0].data.State != selfupdate.StateIdle || first[0].data.Current != "v1.0.0" {
		t.Fatalf("first %+v", first[0].data)
	}
	go func() { _, _ = b.CheckUpdate(false) }()
	evs := readEvents(t, sc, 2)
	if evs[0].data.State != selfupdate.StateChecking || evs[1].data.State != selfupdate.StateFailed || evs[1].data.Error == "" {
		t.Fatalf("events %+v", evs)
	}
	if !(first[0].data.Seq < evs[0].data.Seq && evs[0].data.Seq < evs[1].data.Seq) || evs[1].id != fmt.Sprint(evs[1].data.Seq) {
		t.Fatalf("ids %s %s %s", first[0].id, evs[0].id, evs[1].id)
	}
	done := make(chan struct{})
	go func() {
		for sc.Scan() {
		}
		close(done)
	}()
	CloseStreams(h)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream still open after CloseStreams")
	}
	// 关闭之后新连接照常可用（交接失败、收回监听时）。
	resp2, err := http.Get(srv.URL + "/api/update/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if again := readEvents(t, bufio.NewScanner(resp2.Body), 1); again[0].data.State != selfupdate.StateFailed {
		t.Fatalf("%+v", again[0].data)
	}
}

// 后端没有推送接口：返回 404，页面退回轮询。
func TestUpdateEventsWithoutWatcher(t *testing.T) {
	b := newFakeUpdate(t)
	srv := httptest.NewServer(Handler(struct {
		*fake
		UpdateBackend
	}{b.fake, b}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/update/events")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// 页面里的 upSize / upSizePair / upSpeed / upEta 与 updateview 的 Go 版本输出一致（控制台和页面数字相同）。
// 需要 node；没有就跳过。
func TestPageFormattersMatchConsole(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	raw, err := os.ReadFile("page.html")
	if err != nil {
		t.Fatal(err)
	}
	var src strings.Builder
	src.WriteString(`const MESSAGES = {zh: {upSec: "{n} 秒", upMin: "{n} 分钟"}}; const t = (k, v) => MESSAGES.zh[k].replace("{n}", v.n);` + "\n")
	for _, re := range []string{`(?m)^const MIB = .*$`, `(?ms)^function upSize\(n\) \{.*?^\}`, `(?ms)^function upSizePair\(done, total\) \{.*?^\}`, `(?m)^function upSpeed\(bps\).*$`, `(?m)^function upEta\(sec\).*$`} {
		m := regexp.MustCompile(re).Find(raw)
		if m == nil {
			t.Fatalf("page.html has no %s", re)
		}
		src.Write(m)
		src.WriteString("\n")
	}
	var inputs [][3]int64
	var want []string
	sizes := []int64{0, 1, 300, 511, 512, 1023, 1024, 65536, 1048575, 1048576, 1363149, 6501171, 15623782, 52428800, 1 << 30}
	for i, n := range sizes {
		total := sizes[(i+5)%len(sizes)]
		eta := []int64{0, 1, 7, 59, 89, 90, 91, 119, 120, 3599, 3600}[i%11]
		inputs = append(inputs, [3]int64{n, total, eta})
		want = append(want, updateview.SizePair(n, total)+"|"+updateview.Speed(n)+"|"+updateview.ETA(eta)+"|"+updateview.Size(total))
	}
	js, _ := json.Marshal(inputs)
	src.WriteString("for (const [d, tot, e] of " + string(js) + ") console.log(upSizePair(d, tot) + '|' + upSpeed(d) + '|' + upEta(e) + '|' + upSize(tot));\n")
	out, err := exec.Command(node, "-e", src.String()).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		for i := range want {
			if i >= len(got) || got[i] != want[i] {
				t.Errorf("input %v: page %q, console %q", inputs[i], at(got, i), want[i])
			}
		}
	}
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<missing>"
}
