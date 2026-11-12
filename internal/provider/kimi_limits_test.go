package provider

import "testing"

func TestKimiVariantLimits(t *testing.T) {
	cases := []struct {
		model string
		ctx   int
		out   int
	}{
		{"kimi-k3", 1000000, 131072},
		{"kimi-k3.1-pro", 1000000, 131072},
		{"kimi-k3.1-pro-highspeed", 1000000, 131072},
		{"k3.1-pro", 1000000, 131072},
		{"moonshotai/kimi-k3.1-pro", 1000000, 131072},
		{"kimi-k3-256k", 262144, 8192},
		{"k3-256k", 262144, 8192},
		{"kimi-k2.5", 262144, 32768},
		{"kimi-k2-5", 262144, 32768},
		{"kimi-k2", 262144, 8192},
		{"moonshot-v1-128k", 262144, 8192},
		{"deepseek-chat", 0, 0},
		{"qwen3-coder-plus", 0, 0},
	}
	for _, c := range cases {
		gc, go_ := kimiVariantLimits(c.model)
		if gc != c.ctx || go_ != c.out {
			t.Errorf("%s: got (%d,%d) want (%d,%d)", c.model, gc, go_, c.ctx, c.out)
		}
	}
}

func TestQwenVariantLimitsIncludesQwen4(t *testing.T) {
	c, o := qwenVariantLimits("qwen4-max")
	if c != 1000000 || o != 65536 {
		t.Fatalf("qwen4-max: got (%d,%d)", c, o)
	}
	c, o = qwenVariantLimits("qwen4-max-preview")
	if c != 1000000 || o != 393216 {
		t.Fatalf("qwen4-max-preview: got (%d,%d)", c, o)
	}
}
