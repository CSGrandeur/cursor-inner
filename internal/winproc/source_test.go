package winproc

import (
	"go/build"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var consoleTool = regexp.MustCompile(`exec\.Command(Context)?\((ctx,\s*)?"(?i:powershell|pwsh|taskkill|tasklist|where|wmic|certutil|schtasks|ipconfig|cmd|reg|netsh|sc|wsl)(\.exe)?"`)

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

	for _, pkgDir := range []string{"../grokbot", "../codexbot", "../antigravitylaunch"} {
		entries, err := os.ReadDir(pkgDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			ok, err := ctx.MatchFile(pkgDir, e.Name())
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				continue
			}
			full := filepath.Join(pkgDir, e.Name())
			src, err := os.ReadFile(full)
			if err != nil {
				t.Fatal(err)
			}
			code := stripComments(full, src)
			for _, bad := range []string{"Quiet(", "QuietContext(", "exec.Command", "os/exec"} {
				if strings.Contains(code, bad) {
					t.Errorf("%s contains %q: periodic takeover code must not spawn subprocesses", full, bad)
				}
			}
			lower := strings.ToLower(code)
			for _, bad := range []string{"wsl.exe", "--proxy_server_url", "cursor_inner_ls_proxy="} {
				if strings.Contains(lower, bad) {
					t.Errorf("%s contains %q: must not invoke or wrap WSL", full, bad)
				}
			}
			if strings.Contains(lower, "wslenv") && filepath.ToSlash(full) != "../antigravitylaunch/env.go" {
				t.Errorf("%s contains %q: WSLENV may only be managed in ../antigravitylaunch/env.go", full, "wslenv")
			}
			if strings.Contains(lower, "cursor_inner_ls_proxy") && filepath.ToSlash(full) != "../antigravitylaunch/env.go" {
				t.Errorf("%s contains %q: legacy LS wrapper var may only be stripped in ../antigravitylaunch/env.go", full, "cursor_inner_ls_proxy")
			}
		}
	}

	tunWin, err := os.ReadFile("../tunnel/tunnel_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(tunWin), "Get-DnsClientServerAddress") {
		t.Error("../tunnel/tunnel_windows.go DetectDNS must use Win32 API instead of PowerShell")
	}
}

func stripComments(name string, src []byte) string {
	var s scanner.Scanner
	fset := token.NewFileSet()
	s.Init(fset.AddFile(name, fset.Base(), len(src)), src, nil, 0)
	var b strings.Builder
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if lit != "" {
			b.WriteString(lit)
		} else {
			b.WriteString(tok.String())
		}
		b.WriteByte(' ')
	}
	return b.String()
}
