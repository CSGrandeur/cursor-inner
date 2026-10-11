package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ErrChecksum 表示下载内容和清单里的 sha256 或大小对不上。文件已删掉，不会被使用。
var ErrChecksum = errors.New("下载内容校验失败")

// Mirror 是一个可以下载安装包的地方：一个更新源加上它验过清单时用的网络路径。
type Mirror struct {
	Name  string
	Route Route
	URLs  []string
}

// MirrorFailure 记录某个镜像为什么没用上，写进日志和界面。
type MirrorFailure struct {
	Mirror string
	Err    error
}

// DownloadMirrors 依次从各镜像下载，边下边算 sha256，大小和摘要都对上才改名为最终文件并返回路径和用上的镜像名。
// 某个镜像给的文件对不上（被篡改、不完整）就删掉、换下一个；全部失败才报错，校验失败优先报 ErrChecksum。
// 任何失败都不留下可执行文件。
func DownloadMirrors(ctx context.Context, mirrors []Mirror, a Asset, dir string, progress func(done, total int64)) (string, string, []MirrorFailure, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", nil, err
	}
	var fails []MirrorFailure
	var checksum error
	for _, m := range mirrors {
		for _, u := range m.URLs {
			path, err := downloadOne(ctx, m.Route, u, a, dir, progress)
			if err == nil {
				return path, m.Name, fails, nil
			}
			fails = append(fails, MirrorFailure{Mirror: m.Name, Err: err})
			if errors.Is(err, ErrChecksum) && checksum == nil {
				checksum = err
			}
			if ctx.Err() != nil {
				return "", "", fails, ctx.Err()
			}
		}
	}
	if checksum != nil {
		return "", "", fails, checksum
	}
	if len(fails) == 0 {
		return "", "", nil, errors.New("没有下载地址")
	}
	var errs []error
	for _, f := range fails {
		errs = append(errs, fmt.Errorf("%s: %w", f.Mirror, f.Err))
	}
	return "", "", fails, errors.Join(errs...)
}

// Download 是只有一个镜像时的 DownloadMirrors。
func Download(ctx context.Context, r Route, urls []string, a Asset, dir string, progress func(done, total int64)) (string, error) {
	path, _, _, err := DownloadMirrors(ctx, []Mirror{{Name: r.Name, Route: r, URLs: urls}}, a, dir, progress)
	return path, err
}

func downloadOne(ctx context.Context, r Route, url string, a Asset, dir string, progress func(done, total int64)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "cursor-inner-updater")
	resp, err := r.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 %s：HTTP %d", a.Name, resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, "download-*.part")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	w := io.MultiWriter(tmp, h)
	var done int64
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			done += int64(n)
			if done > a.Size {
				return "", fmt.Errorf("%w：%s 比清单写的 %d 字节大", ErrChecksum, a.Name, a.Size)
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return "", err
			}
			if progress != nil {
				progress(done, a.Size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
	}
	if done != a.Size {
		return "", fmt.Errorf("%w：%s 只有 %d 字节，清单写的是 %d", ErrChecksum, a.Name, done, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, a.SHA256) {
		return "", fmt.Errorf("%w：%s 的 sha256 是 %s，清单写的是 %s", ErrChecksum, a.Name, got, a.SHA256)
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	final := filepath.Join(dir, "staged-"+a.Name)
	_ = os.Remove(final)
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", err
	}
	_ = os.Chmod(final, 0o755)
	ok = true
	return final, nil
}
