package egress

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// 内嵌脚本必须和 scripts/strict-egress.ps1 一字不差，免得两份分叉。
func TestEmbeddedScriptMatchesScriptsCopy(t *testing.T) {
	other, err := os.ReadFile("../../scripts/strict-egress.ps1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(other, Script) {
		t.Fatal("internal/egress/strict-egress.ps1 and scripts/strict-egress.ps1 differ")
	}
	if !bytes.HasPrefix(Script, []byte("\xef\xbb\xbf")) {
		t.Fatal("script needs a UTF-8 BOM for Windows PowerShell 5")
	}
}

func TestArgumentsRejectUnsafeProxy(t *testing.T) {
	a, _ := Arguments(`C:\t\s.ps1`, true, "http://127.0.0.1:1080")
	if !strings.HasSuffix(a, `-Enable -Proxy "http://127.0.0.1:1080"`) {
		t.Fatal(a)
	}
	for _, bad := range []string{`http://x" -Command "calc`, "http://u:p@h:1", "socks5://h:1", "http://h:1; rm"} {
		a, _ := Arguments(`C:\t\s.ps1`, true, bad)
		if strings.Contains(a, "-Proxy") {
			t.Fatalf("%q leaked into %s", bad, a)
		}
	}
	d, _ := Arguments(`C:\t\s.ps1`, false, "http://127.0.0.1:1080")
	if !strings.HasSuffix(d, "-Disable") {
		t.Fatal(d)
	}
}
