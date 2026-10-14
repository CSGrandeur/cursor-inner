//go:build windows

package grokbot

import "testing"

func TestCloseStopsManagedGrokWithoutRestart(t *testing.T) {
	oldList, oldStop, oldStart := listGrok, stopGrok, startGrok
	t.Cleanup(func() { listGrok, stopGrok, startGrok = oldList, oldStop, oldStart })

	stopped, started := false, false
	listGrok = func() ([]process, error) {
		if stopped {
			return nil, nil
		}
		cmd := `"C:\Grok\Grok Bot.exe" ` + joinArgs(ProxyArgs("http://127.0.0.1:20378"))
		return []process{{ExecutablePath: `C:\Grok\Grok Bot.exe`, CommandLine: cmd}}, nil
	}
	stopGrok = func() error { stopped = true; return nil }
	startGrok = func(string, string) error { started = true; return nil }

	if !ManagedRunning("http://127.0.0.1:20378") {
		t.Fatal("带代理参数的 Grok 应算接管中")
	}
	closed, err := Close("http://127.0.0.1:20378")
	if err != nil || !closed {
		t.Fatalf("closed=%v err=%v", closed, err)
	}
	if !stopped || started {
		t.Fatalf("应只结束不重开：stopped=%v started=%v", stopped, started)
	}
}

func TestCloseLeavesUserLaunchedGrokAlone(t *testing.T) {
	oldList, oldStop := listGrok, stopGrok
	t.Cleanup(func() { listGrok, stopGrok = oldList, oldStop })
	listGrok = func() ([]process, error) {
		return []process{{ExecutablePath: `C:\Grok\Grok Bot.exe`, CommandLine: `"C:\Grok\Grok Bot.exe"`}}, nil
	}
	stopGrok = func() error { t.Fatal("不该结束用户自己启动的 Grok"); return nil }
	if ManagedRunning("") {
		t.Fatal("裸启动的 Grok 不算接管中")
	}
	if closed, err := Close(""); err != nil || closed {
		t.Fatalf("closed=%v err=%v", closed, err)
	}
}
