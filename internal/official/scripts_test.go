package official

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var exactLine = regexp.MustCompile(`(?m)^\$OfficialExactHosts = @\(([^)]*)\)`)
var suffixLine = regexp.MustCompile(`(?m)^\$OfficialSuffixes = @\(([^)]*)\)`)

// 脚本里的官方域名清单要和 Go 里的一致；Windows PowerShell 5 需要 BOM 才能正确读中文。
func TestScriptsShareOfficialList(t *testing.T) {
	want := "'" + strings.Join(Suffixes, "', '") + "'"
	for _, name := range []string{"grok-leak-check.ps1", "strict-egress.ps1"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(raw, []byte("\xef\xbb\xbf")) {
			t.Errorf("%s needs a UTF-8 BOM", name)
		}
		wantHosts := "'" + strings.Join(Hosts, "', '") + "'"
		if h := exactLine.FindSubmatch(raw); h == nil || string(h[1]) != wantHosts {
			t.Errorf("%s: exact hosts %q, want %q", name, h, wantHosts)
		}
		m := suffixLine.FindSubmatch(raw)
		if m == nil || string(m[1]) != want {
			t.Errorf("%s: official suffixes %q, want %q", name, m, want)
		}
	}
}

// 泄漏检查要覆盖两种代理范围：官方被直连判失败；第三方全局应走代理、仅官方应直连。
func TestLeakCheckCoversBothScopes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "grok-leak-check.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, part := range []string{
		"[ValidateSet('auto', 'global', 'official')]",
		"官方端点被 cursor-inner 直连",
		"仅官方模式下第三方应直连",
		"全局模式下第三方应走代理",
		"proxy.scope",
	} {
		if !strings.Contains(s, part) {
			t.Errorf("grok-leak-check.ps1 is missing %q", part)
		}
	}
}

// 有 PowerShell（Windows，或 WSL 里的 powershell.exe）时，确认脚本能被解析。
func TestScriptsParseInPowerShell(t *testing.T) {
	var shell string
	for _, name := range []string{"powershell.exe", "pwsh"} {
		if p, err := exec.LookPath(name); err == nil {
			shell = p
			break
		}
	}
	if shell == "" {
		t.Skip("no PowerShell available")
	}
	for _, name := range []string{"grok-leak-check.ps1", "strict-egress.ps1", "tun-egress.ps1"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		// 经标准输入交给解析器，免得处理 WSL 与 Windows 的路径换算。
		cmd := exec.Command(shell, "-NoProfile", "-NonInteractive", "-Command",
			"[Console]::InputEncoding=[Text.Encoding]::UTF8; $t=[Console]::In.ReadToEnd(); $e=$null; [void][System.Management.Automation.Language.Parser]::ParseInput($t,[ref]$null,[ref]$e); if ($e.Count) { $e | ForEach-Object { $_.Message }; exit 1 }")
		cmd.Stdin = bytes.NewReader(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%s does not parse: %v\n%s", name, err, out)
		}
	}
}
