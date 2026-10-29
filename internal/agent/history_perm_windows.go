//go:build windows

package agent

// tightenPerms 在 Windows 上是空操作。
// Go 在 Windows 上把普通文件 Mode 报成 0666，Perm()&0o077 几乎总为真；
// 若仍 WalkDir+Chmod，会在每次启动时扫完全部 conversations（本机曾到约 9 万 blob），
// 黑屏空控制台要卡十几秒，且 chmod 改不掉“下次还是 0666”，只会越积越慢。
func tightenPerms(string) {}
