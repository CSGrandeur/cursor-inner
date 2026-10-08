package mitm

import (
	"testing"

	"cursor-inner/internal/catalog"
)

func TestLeakedModel(t *testing.T) {
	entries := []catalog.Entry{{ID: "model-12345678", DisplayName: "Mine"}}
	if got := leakedModel([]byte(`{"model":"model-12345678"}`), entries); got != "model-12345678" {
		t.Fatal(got)
	}
	if got := leakedModel([]byte(`{"model":"claude"}`), entries); got != "" {
		t.Fatal(got)
	}
	if got := leakedModel([]byte("short"), []catalog.Entry{{ID: "ab"}}); got != "" {
		t.Fatal(got)
	}
}
