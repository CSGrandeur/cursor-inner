//go:build windows

package grokbot

import (
	"encoding/binary"
	"syscall"
	"unsafe"
)

var procGetExtendedTcpTable = syscall.NewLazyDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const (
	tcpTableOwnerPIDAll  = 5
	mibTCPStateCloseWait = 8
)

// closeWaitCount 数本进程处于 CLOSE_WAIT 的 TCP 连接（IPv4 + IPv6）。失败返回 -1。
func closeWaitCount() int {
	v4, ok4 := countCloseWait(syscall.AF_INET, 24, 0, 20)
	v6, ok6 := countCloseWait(23, 56, 48, 52)
	if !ok4 && !ok6 {
		return -1
	}
	return v4 + v6
}

// countCloseWait 读一张 MIB_TCP(6)TABLE_OWNER_PID。rowSize 为行长，stateOff/pidOff 为状态与 PID 在行内的偏移。
func countCloseWait(family uint32, rowSize, stateOff, pidOff int) (int, bool) {
	var size uint32
	_, _, _ = procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPIDAll, 0)
	for try := 0; try < 3 && size > 0; try++ {
		size += 4096
		buf := make([]byte, size)
		r, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPIDAll, 0)
		if r == 122 { // ERROR_INSUFFICIENT_BUFFER：表在两次调用之间变大了
			continue
		}
		if r != 0 {
			return 0, false
		}
		return parseCloseWait(buf, rowSize, stateOff, pidOff, uint32(pid)), true
	}
	return 0, false
}

func parseCloseWait(buf []byte, rowSize, stateOff, pidOff int, self uint32) int {
	if len(buf) < 4 {
		return 0
	}
	n := int(binary.LittleEndian.Uint32(buf))
	count := 0
	for i := 0; i < n; i++ {
		row := 4 + i*rowSize
		if row+rowSize > len(buf) {
			break
		}
		if binary.LittleEndian.Uint32(buf[row+stateOff:]) == mibTCPStateCloseWait &&
			binary.LittleEndian.Uint32(buf[row+pidOff:]) == self {
			count++
		}
	}
	return count
}
