package dialer

import (
	"testing"

	"cursor-inner/internal/config"
)

func TestNormalizeBareHostIsSocks5(t *testing.T) {
	got, err := Normalize("127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	if got != "socks5://127.0.0.1:1080" {
		t.Fatal(got)
	}
}

func TestEmptyAddressIsOffEvenWhenEnabled(t *testing.T) {
	_, on, err := EffectiveAddress(config.Proxy{Enabled: true, Address: "  "})
	if err != nil || on {
		t.Fatalf("on=%v err=%v", on, err)
	}
	_, on, err = EffectiveAddress(config.Proxy{Enabled: false, Address: "127.0.0.1:1"})
	if err != nil || on {
		t.Fatalf("on=%v err=%v", on, err)
	}
}

func TestRejectSelf(t *testing.T) {
	err := RejectSelf("socks5://127.0.0.1:15721", "http://127.0.0.1:15721")
	if err == nil {
		t.Fatal("expected reject")
	}
	if err := RejectSelf("socks5://127.0.0.1:1080", "http://127.0.0.1:15721"); err != nil {
		t.Fatal(err)
	}
}

func TestForModelDirectWhenFlagOff(t *testing.T) {
	d, err := ForModel(config.Proxy{Enabled: true, Address: "127.0.0.1:1080"}, false)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	_, err = ForModel(config.Proxy{Enabled: true}, true)
	if err == nil {
		t.Fatal("expected error")
	}
}
