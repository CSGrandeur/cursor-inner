// 这个小程序把 Electron 拉起的 language_server.exe 拦下来，
// 补上 --proxy_server_url，并再次钉死 HTTPS_PROXY 等，避免 shellEnvSync 冲掉接管代理。
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const realName = "language_server.exe.cursor-inner-real"

func main() {
	self, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	real := filepath.Join(filepath.Dir(self), realName)
	if _, err := os.Stat(real); err != nil {
		os.Exit(1)
	}
	proxy := strings.TrimSpace(os.Getenv("CURSOR_INNER_LS_PROXY"))
	if proxy == "" {
		for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				proxy = v
				break
			}
		}
	}
	args := append([]string{}, os.Args[1:]...)
	if proxy != "" && !hasFlagPrefix(args, "--proxy_server_url=") {
		args = append([]string{"--proxy_server_url=" + proxy}, args...)
	}
	cmd := exec.Command(real, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = withProxyEnv(os.Environ(), proxy)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		os.Exit(1)
	}
}

func hasFlagPrefix(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

func withProxyEnv(base []string, proxy string) []string {
	if proxy == "" {
		return base
	}
	out := make([]string, 0, len(base)+10)
	skip := map[string]bool{}
	for _, k := range []string{
		"CURSOR_INNER_LS_PROXY",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "GRPC_PROXY",
		"http_proxy", "https_proxy", "all_proxy", "grpc_proxy",
	} {
		skip[strings.ToUpper(k)] = true
	}
	for _, e := range base {
		name, _, _ := strings.Cut(e, "=")
		if skip[strings.ToUpper(name)] {
			continue
		}
		out = append(out, e)
	}
	out = append(out,
		"CURSOR_INNER_LS_PROXY="+proxy,
		"HTTP_PROXY="+proxy,
		"HTTPS_PROXY="+proxy,
		"ALL_PROXY="+proxy,
		"http_proxy="+proxy,
		"https_proxy="+proxy,
		"all_proxy="+proxy,
		"GRPC_PROXY="+proxy,
		"grpc_proxy="+proxy,
	)
	return out
}
