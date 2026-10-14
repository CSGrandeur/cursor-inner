package program

import (
	"strings"
	"testing"
)

// 不认识的参数或 --help 不能当成正常启动去接管：要打印用法并退出。
func TestUnknownArgsAndHelpDoNotStart(t *testing.T) {
	for _, c := range []struct {
		args    []string
		help    bool
		unknown string
	}{
		{[]string{"--help"}, true, ""},
		{[]string{"-h"}, true, ""},
		{[]string{"--debug", "--bogus"}, false, "--bogus"},
		{[]string{"serve"}, false, "serve"},
	} {
		opt := parseArgs(c.args)
		if opt.help != c.help || strings.Join(opt.unknown, " ") != c.unknown {
			t.Errorf("%v => help=%v unknown=%v", c.args, opt.help, opt.unknown)
		}
		code, out := argsExit(opt)
		if code < 0 {
			t.Errorf("%v must exit instead of starting", c.args)
		}
		if c.help && code != 0 || !c.help && code == 0 {
			t.Errorf("%v exit code %d", c.args, code)
		}
		if !strings.Contains(out, "--data-dir") {
			t.Errorf("%v usage missing: %q", c.args, out)
		}
	}
}

func TestKnownArgsStillStart(t *testing.T) {
	opt := parseArgs([]string{"--classic-console", "--debug", "--data-dir", "x", "--verbose", "--no-takeover", "--background", "-psn_0_123"})
	if len(opt.unknown) != 0 || opt.help {
		t.Fatalf("%+v", opt)
	}
	if code, _ := argsExit(opt); code >= 0 {
		t.Fatalf("known args must start, got exit %d", code)
	}
}
