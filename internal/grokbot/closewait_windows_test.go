//go:build windows

package grokbot

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// 造一条本进程的 CLOSE_WAIT：对端关闭、本端不关。
func TestCloseWaitCountWindows(t *testing.T) {
	base := closeWaitCount()
	if base < 0 {
		t.Fatal("GetExtendedTcpTable 失败")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, _ := ln.Accept()
	c.Close() // 对端关，s 不关 → s 处于 CLOSE_WAIT
	defer s.Close()
	for i := 0; i < 50; i++ {
		if closeWaitCount() > base {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("没数到新增的 CLOSE_WAIT，base=%d now=%d", base, closeWaitCount())
}

// 用合成的表检查 IPv4 / IPv6 行的偏移：只数状态为 CLOSE_WAIT 且属于指定进程的行。
func TestParseCloseWaitRows(t *testing.T) {
	build := func(rowSize, stateOff, pidOff int, rows [][2]uint32) []byte {
		buf := make([]byte, 4+len(rows)*rowSize)
		binary.LittleEndian.PutUint32(buf, uint32(len(rows)))
		for i, r := range rows {
			o := 4 + i*rowSize
			binary.LittleEndian.PutUint32(buf[o+stateOff:], r[0])
			binary.LittleEndian.PutUint32(buf[o+pidOff:], r[1])
		}
		return buf
	}
	rows := [][2]uint32{{8, 77}, {8, 78}, {5, 77}, {8, 77}} // state, pid
	if n := parseCloseWait(build(24, 0, 20, rows), 24, 0, 20, 77); n != 2 {
		t.Fatalf("ipv4: %d", n)
	}
	if n := parseCloseWait(build(56, 48, 52, rows), 56, 48, 52, 77); n != 2 {
		t.Fatalf("ipv6: %d", n)
	}
	if n := parseCloseWait(nil, 24, 0, 20, 77); n != 0 {
		t.Fatalf("empty: %d", n)
	}
	// 表被截断：不越界，只数完整的行。
	b := build(24, 0, 20, rows)
	if n := parseCloseWait(b[:len(b)-10], 24, 0, 20, 77); n != 1 {
		t.Fatalf("truncated: %d", n)
	}
}
