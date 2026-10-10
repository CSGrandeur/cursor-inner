package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Swap 把 staged 换成 exe：先把正在运行的 exe 改名为 exe.old（Windows 允许给运行中的
// 可执行文件改名，但不允许覆盖），再把 staged 改名到 exe。两步都是同目录 rename。
// 第二步失败会把旧文件改回去。返回备份路径，供 Revert 使用。
func Swap(exe, staged string) (string, error) {
	backup := exe + ".old"
	if _, err := os.Stat(backup); err == nil {
		if err := os.Remove(backup); err != nil {
			// 上一次的旧文件可能还在被旧进程占用，换个名字。
			backup = fmt.Sprintf("%s.old-%d", exe, time.Now().UnixNano())
		}
	}
	if err := os.Rename(exe, backup); err != nil {
		return "", fmt.Errorf("备份当前程序失败：%w", err)
	}
	if err := moveInto(staged, exe); err != nil {
		if rerr := os.Rename(backup, exe); rerr != nil {
			return "", fmt.Errorf("放置新版本失败（%v），且还原旧版本也失败：%w", err, rerr)
		}
		return "", fmt.Errorf("放置新版本失败：%w", err)
	}
	return backup, nil
}

// Revert 把 exe 换回 backup。新版本被改名为 exe.failed 留作排查。
func Revert(exe, backup string) error {
	failed := exe + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(exe, failed); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("移走新版本失败：%w", err)
	}
	if err := os.Rename(backup, exe); err != nil {
		return fmt.Errorf("还原旧版本失败：%w", err)
	}
	return nil
}

// CleanupOld 删掉以前更新留下的 .old / .failed。旧进程还占着时删不掉，下次再试。
func CleanupOld(exe string) {
	matches, _ := filepath.Glob(exe + ".old*")
	matches = append(matches, exe+".failed")
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

func moveInto(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// 不同卷时退回复制。
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, raw, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Remove(src)
	return nil
}
