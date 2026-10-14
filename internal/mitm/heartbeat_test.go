package mitm

import (
	"testing"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/protox"
	"google.golang.org/protobuf/proto"
)

// 客户端每 5 秒发一次 ClientHeartbeat BidiAppend（带 request id、没有 run/exec 消息）。
// 心跳不能转发官方——官方未登录时回 401，客户端会报 Error resuming chat 并一直重试。
func TestHeartbeatBidiHasNoMessage(t *testing.T) {
	// 与真实心跳一致的字节：field1 "3a00"（hex），field2 = {field1: requestID}，field3 递增。
	id := "50ef8023-8b46-4828-bd6b-084b7fe8f763"
	body := protox.AppendString(nil, 1, "3a00")
	body = protox.AppendBytes(body, 2, protox.AppendString(nil, 1, id))
	body = protox.AppendVarint(body, 3, 1)
	inner := protox.Frame(0, body)

	gotID, client, err := protox.DecodeBidi(inner)
	if err != nil || gotID != id {
		t.Fatalf("decode id=%q err=%v", gotID, err)
	}
	var msg cursorpb.AgentClientMessage
	if err := proto.Unmarshal(client, &msg); err != nil || msg.GetClientHeartbeat() == nil {
		t.Fatalf("heartbeat should parse to ClientHeartbeat, err=%v hb=%v", err, msg.GetClientHeartbeat())
	}
	if msg.GetRunRequest() != nil || msg.GetExecClientMessage() != nil {
		t.Fatal("heartbeat should not carry run or exec")
	}
}
