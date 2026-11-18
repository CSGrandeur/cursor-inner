//go:build !windows && !darwin

package antiaccount

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"time"

	"cursor-inner/internal/i18n"
)

// Linux Secret Service：经 secret-tool 写 service=gemini username=antigravity。
// GNOME 下 agy 从 login 集合读，secret-tool 默认写 default 集合：两个都写，
// login 失败不阻塞（default 成功即可用）。D-Bus 不可达时 10 秒超时退出。
const (
	credTarget = "gemini"
	credUser   = "antigravity"
)

func secretToolStore(collection string, payload []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"store"}
	if collection != "" {
		args = append(args, "--collection="+collection, "--label=Password for 'antigravity' on 'gemini'")
	} else {
		args = append(args, "--label=gemini")
	}
	args = append(args, "service", credTarget, "username", credUser)
	cmd := exec.CommandContext(ctx, "secret-tool", args...)
	cmd.Stdin = bytes.NewReader(payload)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, lookErr := exec.LookPath("secret-tool"); lookErr != nil {
			return i18n.E("没有 secret-tool（安装 libsecret-tools / libsecret 后可写入系统凭据库）", "secret-tool is missing (install libsecret-tools / libsecret to use the system credential store)")
		}
		return i18n.Wrap("写入系统凭据库失败："+string(out)+" ", "Writing the system credential store failed: "+string(out)+" ", err)
	}
	return nil
}

func keyringWrite(a Account) error {
	payload := keyringPayload(a)
	loginErr := secretToolStore("/org/freedesktop/secrets/collection/login", payload)
	defaultErr := secretToolStore("", payload)
	if loginErr != nil && defaultErr != nil {
		return loginErr
	}
	return nil
}

func keyringRead() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "secret-tool", "lookup", "service", credTarget, "username", credUser).Output()
}

func keyringReadInProcess() ([]byte, error) {
	return nil, os.ErrNotExist
}
