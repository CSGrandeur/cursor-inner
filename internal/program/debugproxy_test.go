package program

import (
	"strings"
	"testing"

	"cursor-inner/internal/config"
)

func TestInheritProxy(t *testing.T) {
	configured := config.File{Proxy: config.Proxy{Enabled: true, Address: "127.0.0.1:1080"}}

	store, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := inheritProxy(store, configured); got == "" || !store.Get().Proxy.Enabled || store.Get().Proxy.Address != "127.0.0.1:1080" {
		t.Fatalf("debug store must take the configured proxy, got %q %+v", got, store.Get().Proxy)
	}

	own, _ := config.Load(t.TempDir())
	_ = own.Update(func(f *config.File) error {
		f.Proxy = config.Proxy{Enabled: true, Address: "http://127.0.0.1:3128"}
		return nil
	})
	inheritProxy(own, configured)
	if own.Get().Proxy.Address != "http://127.0.0.1:3128" {
		t.Fatal("an explicitly configured debug proxy must be kept")
	}

	none, _ := config.Load(t.TempDir())
	if got := inheritProxy(none, config.File{}); got != "" || strings.TrimSpace(none.Get().Proxy.Address) != "" {
		t.Fatal("with no proxy anywhere the debug instance stays direct")
	}
}
