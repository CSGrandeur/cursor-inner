//go:build windows

package winproc

import (
	"encoding/binary"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Conn 是一条 TCP 连接（只读快照，供泄漏检查这类周期路径用，不起子进程）。
type Conn struct {
	LocalPort  uint16
	RemoteIP   netip.Addr
	RemotePort uint16
	PID        uint32
	Listen     bool
}

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

const tcpTableOwnerPIDAll = 5

// TCPConnections 用 GetExtendedTcpTable 读 IPv4/IPv6 连接表，进程内完成。
// 周期检查不能用 Get-NetTCPConnection 那类 PowerShell：会拉起控制台黑框。
func TCPConnections() ([]Conn, error) {
	v4, err := tcpTable(windows.AF_INET)
	if err != nil {
		return nil, err
	}
	v6, err := tcpTable(windows.AF_INET6)
	if err != nil {
		return v4, nil
	}
	return append(v4, v6...), nil
}

func tcpTable(family uint32) ([]Conn, error) {
	size := uint32(0)
	// 先问大小；表在两次调用之间可能变化，循环到成功。
	for range 8 {
		buf := make([]byte, size)
		var p unsafe.Pointer
		if size > 0 {
			p = unsafe.Pointer(&buf[0])
		}
		r, _, _ := procGetExtendedTcpTable.Call(uintptr(p), uintptr(unsafe.Pointer(&size)), 0,
			uintptr(family), tcpTableOwnerPIDAll, 0)
		if r == 0 {
			return parseTCPTable(buf, family), nil
		}
		if windows.Errno(r) != windows.ERROR_INSUFFICIENT_BUFFER {
			return nil, windows.Errno(r)
		}
	}
	return nil, windows.ERROR_INSUFFICIENT_BUFFER
}

func parseTCPTable(buf []byte, family uint32) []Conn {
	if len(buf) < 4 {
		return nil
	}
	n := binary.LittleEndian.Uint32(buf)
	rowSize := 24
	if family == windows.AF_INET6 {
		rowSize = 56
	}
	var out []Conn
	for i := uint32(0); i < n; i++ {
		off := 4 + int(i)*rowSize
		if off+rowSize > len(buf) {
			break
		}
		row := buf[off : off+rowSize]
		state := binary.LittleEndian.Uint32(row[0:])
		var addr netip.Addr
		var rport []byte
		if family == windows.AF_INET {
			var a [4]byte
			copy(a[:], row[12:16])
			addr = netip.AddrFrom4(a)
			rport = row[20:24]
		} else {
			var a [16]byte
			copy(a[:], row[24:40])
			addr = netip.AddrFrom16(a)
			rport = row[48:52]
		}
		out = append(out, Conn{
			LocalPort:  bePort(row[8:12]),
			RemoteIP:   addr,
			RemotePort: bePort(rport),
			PID:        binary.LittleEndian.Uint32(row[rowSize-4:]),
			Listen:     state == 2,
		})
	}
	return out
}

// bePort 取 DWORD 里按网络字节序存的端口（高 16 位为 0）。
func bePort(b []byte) uint16 {
	return uint16(b[0])<<8 | uint16(b[1])
}
