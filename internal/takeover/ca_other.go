//go:build !windows

package takeover

import "crypto/x509"

func InstallCA(certPath string, leaf *x509.Certificate) (state, detail string) {
	return "untrusted", "证书安装只在 Windows 上执行。"
}
