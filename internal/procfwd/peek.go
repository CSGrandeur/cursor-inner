package procfwd

import (
	"errors"
	"io"
	"net"
	"strings"
	"time"
)

func readHello(conn net.Conn) (name string, raw []byte, err error) {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	hdr := make([]byte, 5)
	if _, err = io.ReadFull(conn, hdr); err != nil {
		return "", nil, err
	}
	if hdr[0] != 22 {
		return "", nil, errors.New("not a handshake")
	}
	n := int(hdr[3])<<8 | int(hdr[4])
	if n <= 0 || n > 1<<14 {
		return "", nil, errors.New("bad handshake length")
	}
	raw = make([]byte, 5+n)
	copy(raw, hdr)
	if _, err = io.ReadFull(conn, raw[5:]); err != nil {
		return "", nil, err
	}
	name, err = serverName(raw)
	return name, raw, err
}

func serverName(record []byte) (string, error) {
	if len(record) < 5 || record[0] != 22 {
		return "", errors.New("not a handshake")
	}
	n := int(record[3])<<8 | int(record[4])
	if len(record) < 5+n {
		return "", errors.New("short record")
	}
	body := record[5 : 5+n]
	if len(body) < 4 || body[0] != 1 {
		return "", errors.New("not a client hello")
	}
	hsLen := int(body[1])<<16 | int(body[2])<<8 | int(body[3])
	if hsLen < 0 || len(body) < 4+hsLen {
		return "", errors.New("short hello")
	}
	p := body[4 : 4+hsLen]
	if len(p) < 35 {
		return "", errors.New("short hello")
	}
	p = p[34:]
	if len(p) < 1 {
		return "", errors.New("short hello")
	}
	sid := int(p[0])
	if len(p) < 1+sid {
		return "", errors.New("short hello")
	}
	p = p[1+sid:]
	if len(p) < 2 {
		return "", errors.New("short hello")
	}
	cs := int(p[0])<<8 | int(p[1])
	if len(p) < 2+cs {
		return "", errors.New("short hello")
	}
	p = p[2+cs:]
	if len(p) < 1 {
		return "", errors.New("short hello")
	}
	comp := int(p[0])
	if len(p) < 1+comp {
		return "", errors.New("short hello")
	}
	p = p[1+comp:]
	if len(p) < 2 {
		return "", errors.New("no server name")
	}
	extLen := int(p[0])<<8 | int(p[1])
	if len(p) < 2+extLen {
		return "", errors.New("short hello")
	}
	p = p[2 : 2+extLen]
	for len(p) >= 4 {
		typ := int(p[0])<<8 | int(p[1])
		ln := int(p[2])<<8 | int(p[3])
		p = p[4:]
		if len(p) < ln {
			return "", errors.New("short hello")
		}
		data := p[:ln]
		p = p[ln:]
		if typ != 0 {
			continue
		}
		if len(data) < 5 {
			return "", errors.New("bad server name")
		}
		if data[2] != 0 {
			return "", errors.New("bad server name")
		}
		nameLen := int(data[3])<<8 | int(data[4])
		if len(data) < 5+nameLen {
			return "", errors.New("bad server name")
		}
		return strings.TrimSuffix(strings.ToLower(string(data[5:5+nameLen])), "."), nil
	}
	return "", errors.New("no server name")
}

func allowed(name string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	for _, n := range names {
		if name == n {
			return true
		}
	}
	return false
}
