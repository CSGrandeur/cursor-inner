package agent

import (
	"io/fs"
	"os"
	"path/filepath"
)

// tightenPerms 把旧版本留下的对话历史改成只有本人可读写（文件 0600、目录 0700）。
// Windows 上 chmod 只影响只读位，没有副作用。
func tightenPerms(dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if info.Mode().Perm()&0o077 != 0 {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		}
		if info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0 {
			_ = os.Chmod(path, 0o600)
		}
		return nil
	})
}
