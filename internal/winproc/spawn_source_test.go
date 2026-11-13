package winproc

import (
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var bareSpawn = regexp.MustCompile(`exec\.Command(Context)?\(|os\.StartProcess\(|syscall\.StartProcess\(|windows\.CreateProcess\(`)

// spawnAllowed 是确实要自己带控制台或自己设标志的少数位置，每条写明理由。
var spawnAllowed = map[string]string{
	"program/window_windows.go": "经典控制台模式本来就要一个可见的 conhost 窗口",
}

// Windows 构建里所有起子进程的地方都必须走 winproc（Quiet / QuietContext / GUI / Detached），
// 不允许裸用 exec.Command：否则一个忘了设 CREATE_NO_WINDOW 的控制台程序就会定期闪黑框。
func TestWindowsBuildSpawnsOnlyThroughWinproc(t *testing.T) {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH = "windows", "amd64"
	for _, root := range []string{"..", "../../cmd"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "winproc" || d.Name() == "winproctest") {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if ok, err := ctx.MatchFile(filepath.Dir(path), d.Name()); err != nil || !ok {
				return err
			}
			rel := filepath.ToSlash(strings.TrimPrefix(path, "../"))
			if _, ok := spawnAllowed[rel]; ok {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if m := bareSpawn.Find(src); m != nil {
				t.Errorf("%s: bare %s in the Windows build, use winproc.Quiet / GUI / Detached", rel, m)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
