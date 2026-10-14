package mitm

import (
	"bytes"
	"testing"
)

// RFC 6455 示例：key "dGhlIHNhbXBsZSBub25jZQ==" 的 Accept 固定是这个值。
// 本地接管官方拒绝的升级时写错 Accept，客户端会立刻断开，一帧都不发。
func TestWebsocketAcceptMatchesRFC6455(t *testing.T) {
	got := websocketAccept("dGhlIHNhbXBsZSBub25jZQ==")
	want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got != want {
		t.Fatalf("accept %q, want %q", got, want)
	}
}

// 本地 hello 帧是 protobuf { field1: { field1: 1 } }，即 0a 02 08 01。
// 官方 hello 转发失败（未登录）时客户端就靠这个继续发 run。
func TestLocalHelloFrameBytes(t *testing.T) {
	want := []byte{0x0a, 0x02, 0x08, 0x01}
	if !bytes.Equal(localHello(), want) {
		t.Fatalf("hello %x, want %x", localHello(), want)
	}
}
