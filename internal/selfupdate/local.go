package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursor-inner/internal/logx"
)

// LocalResult 是 ApplyLocal 的结果。
type LocalResult struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	DryRun  bool   `json:"dry_run"`
}

// ApplyLocal 把本机的一个程序文件热替换进来，走和在线更新相同的后半程：
// 复制到更新目录并算 sha256 → 预检（--version 能跑、报出版本号）→ 等自定义模型回合空闲
// → 同目录换文件 → 同端口交接（新进程健康检查不过就回滚）。不校验签名：来源是本机文件，
// 由调用方（开发用的 devswap）负责只接受本机用户自己放的请求。dryRun 只做到预检为止。
func (u *Updater) ApplyLocal(ctx context.Context, src string, dryRun bool) (LocalResult, error) {
	var res LocalResult
	res.DryRun = dryRun
	src, err := filepath.Abs(src)
	if err != nil {
		return res, err
	}
	if fi, err := os.Stat(src); err != nil {
		return res, fmt.Errorf("找不到要换上的程序：%w", err)
	} else if fi.IsDir() {
		return res, errors.New("要换上的程序是目录")
	}
	if same, _ := sameFile(src, u.opt.Exe); same {
		return res, errors.New("要换上的程序就是正在运行的这个文件")
	}
	u.mu.Lock()
	if u.running {
		u.mu.Unlock()
		return res, errors.New("更新正在进行")
	}
	u.running = true
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.running = false; u.mu.Unlock() }()

	dir := filepath.Join(u.opt.DataDir, "update")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, err
	}
	staged := filepath.Join(dir, fmt.Sprintf("local-%d%s", time.Now().UnixNano(), filepath.Ext(u.opt.Exe)))
	sum, err := copyHashed(src, staged)
	if err != nil {
		return res, err
	}
	res.SHA256 = sum
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(staged)
		}
	}()
	ver, err := localVersion(ctx, staged)
	if err != nil {
		return res, err
	}
	res.Version = ver
	slog.Info("本机程序已复制并预检", logx.FileOnly, "src", src, "version", ver, "sha256", sum, "dry_run", dryRun)
	if dryRun {
		return res, nil
	}
	if u.opt.Busy != nil && u.opt.Busy() {
		u.set(func(s *Status) { s.State = StateWaitingIdle })
		if err := WaitIdle(ctx, u.opt.Busy, 3*time.Second, time.Second); err != nil {
			u.set(func(s *Status) { s.State = StateIdle })
			return res, err
		}
	}
	u.set(func(s *Status) { s.State = StateHandover })
	backup, err := Swap(u.opt.Exe, staged)
	if err != nil {
		u.set(func(s *Status) { s.State, s.Error = StateFailed, err.Error() })
		return res, err
	}
	keep = true // staged 已经被移到 exe 的位置
	if err := u.opt.Handover(ctx, ver); err != nil {
		if rerr := Revert(u.opt.Exe, backup); rerr != nil {
			err = fmt.Errorf("%v；还原程序文件失败：%v", err, rerr)
		}
		u.set(func(s *Status) { s.State, s.Error = StateFailed, err.Error() })
		return res, err
	}
	u.set(func(s *Status) { s.State = StateDone })
	return res, nil
}

func localVersion(ctx context.Context, exe string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := versionCommand(ctx, exe).Output()
	if err != nil {
		return "", fmt.Errorf("要换上的程序无法在本机启动：%w", err)
	}
	v := strings.TrimSpace(string(out))
	if v == "" || strings.ContainsAny(v, "\r\n\"") || len(v) > 64 {
		return "", fmt.Errorf("要换上的程序没有报出可用的版本号：%q", v)
	}
	return v, nil
}

func copyHashed(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		os.Remove(dst)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sameFile(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(fa, fb), nil
}
