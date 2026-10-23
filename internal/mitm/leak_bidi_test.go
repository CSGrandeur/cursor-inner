package mitm

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"testing"

	"cursor-inner/internal/catalog"
)

// BidiAppend 的 data 是十六进制编码的字节，请求体还可能 gzip 压缩；只查原文会漏，
// 读不懂的自定义模型请求就被交给官方，用户看到 “Unknown model ID”。
func TestLeakedBidiModelSeesHexAndGzip(t *testing.T) {
	const id = "62c2ab93e935104a"
	entries := []catalog.Entry{{ID: id}}
	hexBody := []byte("\x0a\x40" + hex.EncodeToString([]byte("xx"+id+"yy")))
	if leakedModel(hexBody, entries) != "" {
		t.Fatal("precondition: plain scan should miss hex form")
	}
	if got := leakedBidiModel(hexBody, "", entries); got != id {
		t.Fatalf("hex: %q", got)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(hexBody)
	_ = w.Close()
	if got := leakedBidiModel(gz.Bytes(), "gzip", entries); got != id {
		t.Fatalf("gzip: %q", got)
	}
	if got := leakedBidiModel([]byte("nothing here"), "", entries); got != "" {
		t.Fatalf("false positive: %q", got)
	}
}
