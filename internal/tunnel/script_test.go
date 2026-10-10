package tunnel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 嵌入的脚本必须和 public/scripts/tun-egress.ps1 字节一致，避免两处漂移。
func TestEmbeddedScriptMatchesRepo(t *testing.T) {
	// 测试工作目录是包目录 public/internal/tunnel；仓库脚本在 ../../scripts。
	p := filepath.Join("..", "..", "scripts", "tun-egress.ps1")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("找不到仓库脚本：%v", err)
	}
	if string(b) != Script {
		t.Fatal("嵌入脚本与 scripts/tun-egress.ps1 不一致")
	}
}

func TestScriptSanity(t *testing.T) {
	for _, want := range []string{"check -c", "RunAs", "ZipSha256", "Get-NetAdapter", "Stop-Tun"} {
		if !strings.Contains(Script, want) {
			t.Fatalf("脚本缺少 %q", want)
		}
	}
}
