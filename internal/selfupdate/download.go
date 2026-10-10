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

// Download 用选定的路径下载文件到 dir 下的临时文件，边下边算 sha256，
// 大小和摘要都对上才改名为最终文件并返回路径。任何失败都不留下可执行文件。
func Download(ctx context.Context, r Route, urls []string, a Asset, dir string, progress func(done, total int64)) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var last error
	for _, u := range urls {
		path, err := downloadOne(ctx, r, u, a, dir, progress)
		if err == nil {
			return path, nil
		}
		last = err
		if errors.Is(err, ErrChecksum) || ctx.Err() != nil {
			return "", err
		}
	}
	if last == nil {
		last = errors.New("没有下载地址")
	}
	return "", last
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
