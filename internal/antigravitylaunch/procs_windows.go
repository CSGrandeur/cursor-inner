//go:build windows

package antigravitylaunch

// Running 报告是否有 Antigravity 在运行（不限是否被接管）。
func Running() bool {
	procs, err := listAnti()
	return err == nil && len(procs) > 0
}

// StopAll 结束全部 Antigravity 主进程（账号切换用）。只逐个结束 Antigravity.exe，
// 严禁 KillTree：WSL 模式下会误杀 wsl.exe 并拖挂 WSLService。没在运行返回 nil。
func StopAll() error {
	applyMu.Lock()
	defer applyMu.Unlock()
	procs, err := listAnti()
	if err != nil {
		return err
	}
	if len(procs) == 0 {
		return nil
	}
	return stopAll()
}

// ExePath 返回正在运行的 Antigravity 可执行文件路径；没在运行返回空串。
func ExePath() string {
	procs, err := listAnti()
	if err != nil {
		return ""
	}
	for _, p := range procs {
		if p.ExecutablePath != "" {
			return p.ExecutablePath
		}
	}
	return ""
}
