//go:build windows

package codexbot

// Launch 打开已安装的 Codex 桌面版。已有桌面进程在跑时不再开一份。
// proxyURL 非空时带上与 Apply 相同的参数和环境变量。
func Launch(proxyURL string) error {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := running()
	if err != nil {
		return err
	}
	if len(desktopShells(procs)) > 0 {
		return nil
	}
	path, err := findCodex(desktopCandidates(), desktopLookups())
	if err != nil {
		return err
	}
	return start(path, proxyURL)
}
