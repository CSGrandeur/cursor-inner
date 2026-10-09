//go:build windows

package grokbot

// Launch 打开已安装的 Grok Bot。已有进程在跑时不再开一份。
// proxyURL 非空时带上与 Apply 相同的代理参数和环境变量。
func Launch(proxyURL string) error {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := running()
	if err != nil {
		return err
	}
	if len(procs) > 0 {
		return nil
	}
	path, err := findGrok(windowsCandidates(), []func() (string, error){whereGrok, registryGrok})
	if err != nil {
		return err
	}
	return start(path, proxyURL)
}
