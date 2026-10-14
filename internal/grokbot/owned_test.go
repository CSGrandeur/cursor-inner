package grokbot

import "testing"

// 另一个数据目录的实例退出时，不能关掉别的实例接管（代理端口不同）的 Grok。
func TestOwnedByOnlyMatchesOwnProxy(t *testing.T) {
	mine := "http://127.0.0.1:41001"
	other := "http://127.0.0.1:42002"
	cmd := "\"Grok Bot.exe\" " + joinArgs(ProxyArgs(other))
	if OwnedBy([]string{cmd}, mine) {
		t.Fatal("别的实例的代理不该算本实例接管")
	}
	if !OwnedBy([]string{"\"Grok Bot.exe\" " + joinArgs(ProxyArgs(mine))}, mine) {
		t.Fatal("带本实例代理参数的应算接管")
	}
	if !OwnedBy([]string{cmd}, "") {
		t.Fatal("旧标记（没记代理）退回旧判断")
	}
	if OwnedBy([]string{"\"Grok Bot.exe\""}, "") {
		t.Fatal("裸启动的不算")
	}
}
