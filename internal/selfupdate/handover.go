package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// 交接协议（旧进程 = 正在运行的版本，新进程 = 刚换上的版本），全程走数据目录下 update/handover/ 里的文件：
//
//	旧：写 plan.json → 拉起新进程（--handover plan.json，与旧进程生命周期无关）
//	新：读配置、准备证书 → 写 ready.json（pid）                    ← 旧进程此时仍在服务，失败零影响
//	旧：停止接受新连接（关监听，已建立的连接继续）、释放单实例 → 写 released
//	新：拿单实例、在同一组地址上监听、接回接管状态 → 写 ok.json
//	旧：健康检查新进程 → 通过：写 succession.json，排空旧连接后退出（不还原接管）
//	                     → 失败：结束新进程、重新监听同一组地址、拿回单实例，继续服务（回滚）
//
// 端口不变，所以 Cursor 的 settings.json、Grok 的启动参数都不用改，两者都不用重启。
// 监听切换的空窗只有「关监听 → 新进程 bind」这一下，通常是毫秒级；这期间到达的新连接会被拒绝，
// 客户端重试即可，绝不会改成直连。

// Plan 是旧进程交给新进程的状态。
type Plan struct {
	Token          string `json:"token"`
	FromPID        int    `json:"from_pid"`
	FromVersion    string `json:"from_version"`
	ToVersion      string `json:"to_version"`
	WebAddr        string `json:"web_addr"`
	MitmAddr       string `json:"mitm_addr,omitempty"`
	TakeoverActive bool   `json:"takeover_active"`
}

// Ack 是新进程接管完成后的回执。
type Ack struct {
	Token   string `json:"token"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
	WebURL  string `json:"web_url"`
}

// Succession 记录一次成功的交接。守护进程据此判断：被监视的旧进程退出是交接，不是退出，不要还原接管。
type Succession struct {
	FromPID int       `json:"from_pid"`
	ToPID   int       `json:"to_pid"`
	At      time.Time `json:"at"`
}

// Dir 是交接目录。
type Dir string

func HandoverDir(dataDir string) Dir { return Dir(filepath.Join(dataDir, "update", "handover")) }

func (d Dir) path(name string) string { return filepath.Join(string(d), name) }
func (d Dir) PlanPath() string        { return d.path("plan.json") }

func (d Dir) reset() error {
	for _, n := range []string{"plan.json", "ready.json", "released", "ok.json", "abort", "succession.json"} {
		if err := os.Remove(d.path(n)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.MkdirAll(string(d), 0o700)
}

func writeJSON(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// waitFile 轮询直到文件出现、ctx 结束或 stop 返回真。
func waitFile(ctx context.Context, path string, stop func() bool) error {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if stop != nil && stop() {
			return errAborted
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

var errAborted = errors.New("交接被取消")

// ErrRolledBack 表示新进程没能接管，旧进程已恢复服务。
var ErrRolledBack = errors.New("新版本没有通过健康检查，已回滚到当前版本")

// Releaser 是旧进程交出、收回监听的能力。Release 只停止接受新连接，已建立的连接继续服务。
type Releaser interface {
	Release() error
	Reclaim() error
}

// Coordinator 是旧进程一侧。
type Coordinator struct {
	Dir          Dir
	Spawn        func(plan Plan) error
	Kill         func(pid int) error
	Res          Releaser
	Health       func(ctx context.Context, ack Ack) error
	Alive        func(pid int) bool // 可选：新进程提前退出时不必等满超时
	ReadyTimeout time.Duration      // 新进程准备好（尚未接管）
	OKTimeout    time.Duration      // 交出监听后到新进程接管完成
}

// Run 执行交接。返回 nil 表示新进程已接管，调用方应排空连接后退出（不还原接管）。
// 返回错误时旧进程仍在（或已恢复）服务：ready 之前失败什么都没交出去；之后失败已 Reclaim。
func (c *Coordinator) Run(ctx context.Context, plan Plan) (Ack, error) {
	if err := c.Dir.reset(); err != nil {
		return Ack{}, err
	}
	if err := writeJSON(c.Dir.PlanPath(), plan); err != nil {
		return Ack{}, err
	}
	if err := c.Spawn(plan); err != nil {
		return Ack{}, fmt.Errorf("启动新版本失败：%w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, c.ReadyTimeout)
	err := waitFile(rctx, c.Dir.path("ready.json"), nil)
	cancel()
	var ready Ack
	if err == nil {
		err = readJSON(c.Dir.path("ready.json"), &ready)
	}
	if err == nil && ready.Token != plan.Token {
		err = errors.New("交接令牌不符")
	}
	if err != nil {
		c.abort(ready.PID)
		return Ack{}, fmt.Errorf("新版本没有准备好：%w", err)
	}

	if err := c.Res.Release(); err != nil {
		_ = c.Res.Reclaim()
		c.abort(ready.PID)
		return Ack{}, fmt.Errorf("交出监听失败：%w", err)
	}
	if err := os.WriteFile(c.Dir.path("released"), nil, 0o600); err != nil {
		return Ack{}, c.rollback(ready.PID, err)
	}
	octx, cancel := context.WithTimeout(ctx, c.OKTimeout)
	defer cancel()
	var gone func() bool
	if c.Alive != nil && ready.PID > 0 {
		gone = func() bool { return !c.Alive(ready.PID) }
	}
	if err := waitFile(octx, c.Dir.path("ok.json"), gone); err != nil {
		return Ack{}, c.rollback(ready.PID, err)
	}
	var ack Ack
	if err := readJSON(c.Dir.path("ok.json"), &ack); err != nil {
		return Ack{}, c.rollback(ready.PID, err)
	}
	if ack.Token != plan.Token || ack.Version != plan.ToVersion {
		return Ack{}, c.rollback(ack.PID, fmt.Errorf("回执不符：%s", ack.Version))
	}
	if err := c.Health(octx, ack); err != nil {
		return Ack{}, c.rollback(ack.PID, err)
	}
	if err := writeJSON(c.Dir.path("succession.json"), Succession{FromPID: plan.FromPID, ToPID: ack.PID, At: time.Now()}); err != nil {
		return Ack{}, c.rollback(ack.PID, err)
	}
	return ack, nil
}

func (c *Coordinator) abort(pid int) {
	_ = os.WriteFile(c.Dir.path("abort"), nil, 0o600)
	if pid > 0 && c.Kill != nil {
		_ = c.Kill(pid)
	}
}

func (c *Coordinator) rollback(pid int, cause error) error {
	c.abort(pid)
	// 新进程被结束后它的监听随之释放；Reclaim 内部会对同一地址重试。
	if err := c.Res.Reclaim(); err != nil {
		return fmt.Errorf("%w（原因：%v）；收回监听也失败：%v", ErrRolledBack, cause, err)
	}
	return fmt.Errorf("%w（原因：%v）", ErrRolledBack, cause)
}

// Successor 是新进程一侧。
type Successor struct {
	Dir  Dir
	Plan Plan
}

// JoinHandover 读取旧进程写的计划。
func JoinHandover(planPath string) (*Successor, error) {
	var p Plan
	if err := readJSON(planPath, &p); err != nil {
		return nil, err
	}
	return &Successor{Dir: Dir(filepath.Dir(planPath)), Plan: p}, nil
}

func (s *Successor) aborted() bool {
	_, err := os.Stat(s.Dir.path("abort"))
	return err == nil
}

// Ready 告诉旧进程：配置读好了，可以交出监听。
func (s *Successor) Ready(pid int) error {
	return writeJSON(s.Dir.path("ready.json"), Ack{Token: s.Plan.Token, PID: pid, Version: s.Plan.ToVersion})
}

// WaitReleased 等旧进程交出监听。旧进程取消交接时返回错误，新进程应直接退出。
func (s *Successor) WaitReleased(ctx context.Context) error {
	return waitFile(ctx, s.Dir.path("released"), s.aborted)
}

// Done 写回执。
func (s *Successor) Done(ack Ack) error {
	ack.Token = s.Plan.Token
	return writeJSON(s.Dir.path("ok.json"), ack)
}

// WaitConfirmed 等旧进程确认交接成功（succession.json 指向自己）。新进程只有在此之后才挂守护进程：
// 否则旧进程健康检查失败、结束新进程时，新进程的守护进程会误以为要还原接管。
func (s *Successor) WaitConfirmed(ctx context.Context, pid int) error {
	path := s.Dir.path("succession.json")
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		var got Succession
		if readJSON(path, &got) == nil && got.ToPID == pid && got.FromPID == s.Plan.FromPID {
			return nil
		}
		if s.aborted() {
			return errAborted
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// HandedOver 判断 pid 的退出是不是一次成功交接：有 succession 记录且接班进程还活着。
func HandedOver(dataDir string, pid int) bool {
	var s Succession
	if err := readJSON(HandoverDir(dataDir).path("succession.json"), &s); err != nil {
		return false
	}
	return s.FromPID == pid && s.ToPID > 0 && ProcessAlive(s.ToPID)
}

// Rebind 在同一地址上重试监听，交接空窗里对方可能还没完全释放。
func Rebind(ctx context.Context, listen func() error) error {
	var err error
	for i := 0; i < 150; i++ {
		if err = listen(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(20 * time.Millisecond):
		}
	}
	return err
}
