package procfwd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	markerBegin = "# cursor-inner-direct"
	markerEnd   = "# end cursor-inner-direct"
)

// 这些名字会被 Cursor 的子进程自己解析并直连，不读 http.proxy。
// api2 和 api5 不在这里：它们要留给本机代理解密。
var names = []string{
	"api3.cursor.sh",
	"api4.cursor.sh",
	"repo42.cursor.sh",
	"us-only.gcpp.cursor.sh",
	"us-eu.gcpp.cursor.sh",
	"us-asia.gcpp.cursor.sh",
}

func hostsPath() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return filepath.Join(root, "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

func block() string {
	var b strings.Builder
	b.WriteString(markerBegin)
	b.WriteByte('\n')
	for _, name := range names {
		b.WriteString("127.0.0.1 ")
		b.WriteString(name)
		b.WriteByte('\n')
	}
	b.WriteString(markerEnd)
	b.WriteByte('\n')
	return b.String()
}

func RemoveBlock(content string) string {
	for {
		i := strings.Index(content, markerBegin)
		if i < 0 {
			return content
		}
		rel := strings.Index(content[i:], markerEnd)
		if rel < 0 {
			return content[:i]
		}
		j := i + rel + len(markerEnd)
		if j < len(content) && content[j] == '\r' {
			j++
		}
		if j < len(content) && content[j] == '\n' {
			j++
		}
		content = content[:i] + content[j:]
	}
}

func ApplyBlock(content string) string {
	rest := strings.TrimLeft(RemoveBlock(content), "\r\n")
	if rest == "" {
		return block()
	}
	return block() + rest
}

func splitBOM(raw []byte) (bom, rest []byte) {
	if bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		return raw[:3], raw[3:]
	}
	return nil, raw
}

func readHosts(path string) (bom []byte, content string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	bom, rest := splitBOM(raw)
	return bom, string(rest), nil
}

func writeHostsFile(path string, bom []byte, content string) error {
	data := append(append([]byte{}, bom...), []byte(content)...)
	return commitHosts(path, data)
}

func installHosts(path string) error {
	bom, content, err := readHosts(path)
	if err != nil {
		return err
	}
	return writeHostsFile(path, bom, ApplyBlock(content))
}

func removeHosts(path string) error {
	bom, content, err := readHosts(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cleaned := RemoveBlock(content)
	if cleaned == content {
		return nil
	}
	return writeHostsFile(path, bom, cleaned)
}

func hostsHeld(path string) bool {
	_, content, err := readHosts(path)
	if err != nil {
		return false
	}
	return strings.Contains(content, markerBegin)
}

// MarkerPresent 报告系统 hosts 里是否还有直连转发。删不掉时本机要继续听 443。
func MarkerPresent() bool {
	return hostsHeld(hostsPath())
}

// Restore 删掉系统 hosts 里的直连转发。接管进程崩溃后由下次启动或守护进程调用。
func Restore() error {
	if err := removeHosts(hostsPath()); err != nil {
		return err
	}
	flushDNS()
	return nil
}

func flushDNS() {
	if runtime.GOOS != "windows" {
		return
	}
	_ = exec.Command("ipconfig", "/flushdns").Run()
}
