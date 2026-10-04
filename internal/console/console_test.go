package console

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

var ansi = regexp.MustCompile(`\x1b\][^\x1b]*\x1b\\|\x1b\[[0-9;?]*[A-Za-z]|\x1b[78]`)

func TestHeaderKeepsClickableURLWithinWidth(t *testing.T) {
	url := "http://127.0.0.1:52341"
	header := renderHeader(url, "v0.1.0", Status{Takeover: true, Proxy: "socks5://127.0.0.1:1080", Models: 3}, 40)
	if !strings.Contains(header, "\x1b]8;;"+url+"\x1b\\") {
		t.Fatal("url is not an OSC 8 hyperlink")
	}
	for i, row := range strings.Split(header, "\r\n") {
		if w := width(ansi.ReplaceAllString(row, "")); w > 40 {
			t.Fatalf("row %d width %d > 40: %q", i, w, row)
		}
	}
}

func TestClipCountsWideRunes(t *testing.T) {
	if got := clip("自定义模型", 5); got != "自定" {
		t.Fatalf("%q", got)
	}
	if width("接管 on") != 7 {
		t.Fatal(width("接管 on"))
	}
}

func TestPlainOutputWhenNotTerminal(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	var file bytes.Buffer
	c := New(out, &file)
	c.Start("http://127.0.0.1:1", "dev", nil)
	_, _ = c.Write([]byte("接管代理 http://127.0.0.1:2\n"))
	c.Close()
	raw, _ := os.ReadFile(out.Name())
	if strings.Contains(string(raw), "\x1b") {
		t.Fatalf("escape codes in redirected output: %q", raw)
	}
	if !strings.Contains(string(raw), "http://127.0.0.1:1") || !strings.Contains(file.String(), "接管代理") {
		t.Fatalf("out=%q file=%q", raw, file.String())
	}
}
