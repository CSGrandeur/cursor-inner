// Package winproc 在进程内查看和结束 Windows 进程，不启动 powershell、taskkill、where 这类控制台程序。
// 接管开着时每 5 秒要看一次 Grok 和 Codex。每次拉起控制台程序都会带出一个 conhost，
// 即使加了 CREATE_NO_WINDOW，用户仍会看到黑框一闪。
package winproc
