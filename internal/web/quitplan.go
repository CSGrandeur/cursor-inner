package web

// QuitPlan 是退出确认框和一键恢复用的数据。
type QuitPlan struct {
	// Confirm 为真时退出前要弹确认（有程序会被关掉、且没选过不再询问）。
	Confirm bool `json:"confirm"`
	// Closes 是退出时会关掉的程序：cursor、grok、codex。
	Closes []string `json:"closes"`
	// Restore 是上次退出时关掉、可一键恢复的程序：grok、codex。
	Restore []string `json:"restore"`
}
