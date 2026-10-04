//go:build darwin

package takeover

import (
	"crypto/x509"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cursor-inner/internal/i18n"
)

func InstallCA(certPath string, leaf *x509.Certificate) (string, i18n.Text) {
	if trustedOnMac(certPath) {
		return "ready", i18n.T("证书已在钥匙串里设为受信任。", "The certificate is trusted in the keychain.")
	}
	home, _ := os.UserHomeDir()
	keychain := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
	manual := fmt.Sprintf("security add-trusted-cert -r trustRoot -k \"%s\" \"%s\"", keychain, certPath)
	out, err := exec.Command("security", "add-trusted-cert", "-r", "trustRoot", "-k", keychain, certPath).CombinedOutput()
	if err != nil {
		return "untrusted", i18n.Join("\n",
			i18n.Tf("证书还没设为受信任。可以在终端执行：\n%s", "The certificate is not trusted yet. You can run this in Terminal:\n%s", manual),
			i18n.Raw(strings.TrimSpace(string(out))))
	}
	if trustedOnMac(certPath) {
		return "ready", i18n.T("证书已写入登录钥匙串并设为受信任。", "The certificate was added to the login keychain and trusted.")
	}
	return "untrusted", i18n.Tf("已写入钥匙串，但系统仍不信任这张证书。可以在终端执行：\n%s", "The certificate is in the keychain but still not trusted. You can run this in Terminal:\n%s", manual)
}

func trustedOnMac(certPath string) bool {
	return exec.Command("security", "verify-cert", "-c", certPath).Run() == nil
}
