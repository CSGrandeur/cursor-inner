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

var consoleTool = regexp.MustCompile(`exec\.Command(Context)?\((ctx,\s*)?"(?i:powershell|pwsh|taskkill|tasklist|where|wmic|certutil|schtasks|ipconfig|cmd|reg|netsh|sc)(\.exe)?"`)

// Windows 构建里的控制台程序都要走 Quiet；接管期间每 5 秒执行的检查不得起任何子进程。
func TestWindowsBuildStartsNoBareConsoleTool(t *testing.T) {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH = "windows", "amd64"
	root := ".."
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "winproctest" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if ok, err := ctx.MatchFile(filepath.Dir(path), d.Name()); err != nil || !ok {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if m := consoleTool.Find(src); m != nil {
			t.Errorf("%s: %s, use winproc instead", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../grokbot/apply_windows.go", "../codexbot/apply_windows.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "Quiet(") {
			t.Errorf("%s runs every 5 seconds and must not start console programs", path)
		}
	}
}
