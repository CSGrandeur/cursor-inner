//go:build windows

package grokbot

import (
	"errors"
	"testing"
)

// 复现 Grok Bot 0.70 闪退：接管时结束裸启动的 Grok，结束报“拒绝访问”但进程其实都已退出。
// 旧代码直接返回错误，不再重新打开，窗口闪一下就没了。
func TestApplyRestartsWhenStopErrsButNothingLeft(t *testing.T) {
	oldList, oldStop, oldStart := listGrok, stopGrok, startGrok
	t.Cleanup(func() { listGrok, stopGrok, startGrok = oldList, oldStop, oldStart })

	stopped := false
	listGrok = func() ([]process, error) {
		if stopped {
			return nil, nil
		}
		return []process{{ExecutablePath: `C:\Grok\Grok Bot.exe`, CommandLine: `"C:\Grok\Grok Bot.exe"`}}, nil
	}
	stopGrok = func() error { stopped = true; return errors.New("Access is denied.") }
	var started string
	startGrok = func(exe, proxyURL string) error { started = proxyURL; return nil }

	if err := Apply("http://127.0.0.1:20378"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if started != "http://127.0.0.1:20378" {
		t.Fatalf("进程已全部退出，应带代理重新打开，实际 started=%q", started)
	}
}

func TestApplyKeepsStateWhenGrokSurvivesStop(t *testing.T) {
	oldList, oldStop, oldStart := listGrok, stopGrok, startGrok
	t.Cleanup(func() { listGrok, stopGrok, startGrok = oldList, oldStop, oldStart })

	listGrok = func() ([]process, error) {
		return []process{{ExecutablePath: `C:\Grok\Grok Bot.exe`, CommandLine: `"C:\Grok\Grok Bot.exe"`}}, nil
	}
	stopGrok = func() error { return errors.New("Access is denied.") }
	called := false
	startGrok = func(string, string) error { called = true; return nil }

	if err := Apply("http://127.0.0.1:20378"); err == nil {
		t.Fatal("Grok 还在运行，应返回错误")
	}
	if called {
		t.Fatal("Grok 还在运行时不能再开一份")
	}
}
