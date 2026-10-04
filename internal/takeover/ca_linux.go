//go:build linux

package takeover

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cursor-inner/internal/i18n"
)

const caNick = "cursor-inner Local CA"

var systemBundles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/ca-bundle.pem",
	"/etc/ca-certificates/extracted/tls-ca-bundle.pem",
}

// InstallCA 把证书写进系统证书库（Node 侧读取）和 NSS 用户库（Chromium 侧读取）。
// 需要 root 的步骤（写系统库、补装 certutil）合并成一次提权。
func InstallCA(certPath string, leaf *x509.Certificate) (string, i18n.Text) {
	var steps []string
	sysOK := systemHas(leaf)
	if !sysOK {
		script, err := systemScript(certPath)
		if err != nil {
			return "untrusted", i18n.Join("\n", i18n.Of(err), nssStatus(certPath, leaf))
		}
		steps = append(steps, script)
	}
	if !lookPath("certutil") {
		if pkg := nssToolsScript(); pkg != "" {
			steps = append(steps, pkg)
		}
	}
	var privileged i18n.Text
	if len(steps) > 0 {
		privileged = runPrivileged(strings.Join(steps, " && "))
	}
	sysOK = systemHas(leaf)
	nss := nssStatus(certPath, leaf)
	sys := i18n.T("系统证书库：已受信任。", "System store: trusted.")
	if !sysOK {
		sys = i18n.Join("\n", i18n.T("系统证书库：未写入。", "System store: not installed."), privileged)
	}
	if sysOK && nssReady(leaf) {
		return "ready", i18n.Join("\n", sys, nss)
	}
	return "untrusted", i18n.Join("\n", sys, nss)
}

func runPrivileged(script string) i18n.Text {
	var cmd *exec.Cmd
	switch {
	case os.Geteuid() == 0:
		cmd = exec.Command("sh", "-c", script)
	case hasDesktop() && lookPath("pkexec"):
		cmd = exec.Command("pkexec", "sh", "-c", script)
	default:
		return i18n.Tf("需要管理员权限，请在终端执行：\nsudo sh -c '%s'", "Administrator rights are needed. Run this in a terminal:\nsudo sh -c '%s'", script)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return i18n.Join("\n",
			i18n.Tf("提权执行失败，可以在终端执行：\nsudo sh -c '%s'", "The privileged step failed. You can run this in a terminal:\nsudo sh -c '%s'", script),
			i18n.Raw(strings.TrimSpace(string(out))))
	}
	return i18n.Text{}
}

func nssDB() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(home, ".pki", "nssdb")
	return dir, "sql:" + dir, nil
}

func nssReady(leaf *x509.Certificate) bool {
	_, db, err := nssDB()
	return err == nil && lookPath("certutil") && nssHas(db, leaf)
}

func nssStatus(certPath string, leaf *x509.Certificate) i18n.Text {
	dir, db, err := nssDB()
	if err != nil {
		return i18n.T("NSS 证书库：找不到用户目录。", "NSS store: home directory not found.")
	}
	if !lookPath("certutil") {
		return i18n.T("NSS 证书库：缺少 certutil，请安装 libnss3-tools（Debian/Ubuntu）、nss-tools（Fedora）或 nss（Arch）后重启 cursor-inner。",
			"NSS store: certutil is missing. Install libnss3-tools (Debian/Ubuntu), nss-tools (Fedora) or nss (Arch), then restart cursor-inner.")
	}
	if nssHas(db, leaf) {
		return i18n.T("NSS 证书库：已受信任。", "NSS store: trusted.")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return i18n.Of(i18n.Wrap("NSS 证书库：", "NSS store: ", err))
	}
	if _, err := os.Stat(filepath.Join(dir, "cert9.db")); errors.Is(err, os.ErrNotExist) {
		_ = exec.Command("certutil", "-d", db, "-N", "--empty-password").Run()
	}
	_ = exec.Command("certutil", "-d", db, "-D", "-n", caNick).Run()
	out, err := exec.Command("certutil", "-d", db, "-A", "-t", "C,,", "-n", caNick, "-i", certPath).CombinedOutput()
	if err != nil || !nssHas(db, leaf) {
		return i18n.Join(" ", i18n.T("NSS 证书库：写入失败。", "NSS store: could not add the certificate."), i18n.Raw(strings.TrimSpace(string(out))))
	}
	return i18n.T("NSS 证书库：已写入。", "NSS store: certificate added.")
}

func nssHas(db string, leaf *x509.Certificate) bool {
	out, err := exec.Command("certutil", "-d", db, "-L", "-n", caNick, "-a").Output()
	return err == nil && pemContains(out, leaf)
}

func systemScript(certPath string) (string, error) {
	q := shellQuote(certPath)
	switch {
	case lookPath("update-ca-certificates") && isDir("/usr/local/share/ca-certificates"):
		return "cp " + q + " /usr/local/share/ca-certificates/cursor-inner.crt && update-ca-certificates", nil
	case lookPath("update-ca-certificates") && isDir("/etc/pki/trust/anchors"):
		return "cp " + q + " /etc/pki/trust/anchors/cursor-inner.pem && update-ca-certificates", nil
	case lookPath("update-ca-trust") && isDir("/etc/pki/ca-trust/source/anchors"):
		return "cp " + q + " /etc/pki/ca-trust/source/anchors/cursor-inner.pem && update-ca-trust", nil
	case lookPath("trust"):
		return "trust anchor --store " + q, nil
	default:
		return "", i18n.Ef("系统证书库：没有识别出本发行版的证书工具，请手动把 %s 加入系统受信任根证书。",
			"System store: no supported certificate tool found. Add %s to the system's trusted roots manually.", certPath)
	}
}

func nssToolsScript() string {
	switch {
	case lookPath("apt-get"):
		return "DEBIAN_FRONTEND=noninteractive apt-get install -y libnss3-tools"
	case lookPath("dnf"):
		return "dnf install -y nss-tools"
	case lookPath("zypper"):
		return "zypper --non-interactive install mozilla-nss-tools"
	case lookPath("pacman"):
		return "pacman -S --noconfirm --needed nss"
	default:
		return ""
	}
}

func systemHas(leaf *x509.Certificate) bool {
	for _, path := range systemBundles {
		if raw, err := os.ReadFile(path); err == nil && pemContains(raw, leaf) {
			return true
		}
	}
	return false
}

func pemContains(raw []byte, leaf *x509.Certificate) bool {
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			return false
		}
		if block.Type == "CERTIFICATE" && bytes.Equal(block.Bytes, leaf.Raw) {
			return true
		}
	}
}

func hasDesktop() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func shellQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`").Replace(s) + `"`
}
