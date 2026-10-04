//go:build windows

package takeover

import (
	"bytes"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

func InstallCA(certPath string, leaf *x509.Certificate) (state, detail string) {
	thumb := thumbprint(leaf)
	if installed(thumb) {
		return "ready", "证书已在当前用户的受信任根证书颁发机构里。"
	}
	out, err := exec.Command("certutil", "-user", "-addstore", "-f", "Root", certPath).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return "untrusted", fmt.Sprintf("证书还没装进系统。可以自己执行：certutil -user -addstore -f Root \"%s\"\n%s", certPath, text)
	}
	if installed(thumb) {
		return "ready", "证书已装进当前用户的受信任根证书颁发机构。"
	}
	return "untrusted", "certutil 已执行，但没有在用户根存储里找到这张证书。\n" + text
}

func thumbprint(leaf *x509.Certificate) string {
	sum := sha1.Sum(leaf.Raw)
	return hex.EncodeToString(sum[:])
}

func installed(thumb string) bool {
	out, err := exec.Command("certutil", "-user", "-store", "Root").CombinedOutput()
	if err != nil {
		return false
	}
	flat := strings.ToLower(string(bytes.ReplaceAll(out, []byte(" "), nil)))
	return strings.Contains(flat, thumb)
}
