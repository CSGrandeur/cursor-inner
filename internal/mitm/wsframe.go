package mitm

import (
	"encoding/binary"
	"io"
)

func readWSFrame(r io.Reader) (opcode byte, payload, raw []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, nil, err
	}
	raw = append(raw, hdr[:]...)
	opcode = hdr[0] & 0x0f
	ln := int(hdr[1] & 0x7f)
	switch ln {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, raw, err
		}
		raw = append(raw, ext[:]...)
		ln = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, raw, err
		}
		raw = append(raw, ext[:]...)
		ln = int(binary.BigEndian.Uint64(ext[:]))
	}
	var mask []byte
	if hdr[1]&0x80 != 0 {
		mask = make([]byte, 4)
		if _, err = io.ReadFull(r, mask); err != nil {
			return 0, nil, raw, err
		}
		raw = append(raw, mask...)
	}
	payload = make([]byte, ln)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, nil, raw, err
	}
	raw = append(raw, payload...)
	if mask != nil {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, raw, nil
}

func writeWSFrame(w io.Writer, opcode byte, payload []byte) error {
	_, err := w.Write(wsFrameBytes(opcode, payload))
	return err
}

func wsFrameBytes(opcode byte, payload []byte) []byte {
	hdr := []byte{0x80 | opcode, 0}
	switch {
	case len(payload) > 65535:
		hdr[1] = 127
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(len(payload)))
		hdr = append(hdr, ext[:]...)
	case len(payload) >= 126:
		hdr[1] = 126
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(len(payload)))
		hdr = append(hdr, ext[:]...)
	default:
		hdr[1] = byte(len(payload))
	}
	return append(hdr, payload...)
}

func maskWSFrame(payload []byte) []byte {
	var hdr []byte
	switch {
	case len(payload) < 126:
		hdr = []byte{0x82, 0x80 | byte(len(payload))}
	case len(payload) <= 65535:
		hdr = []byte{0x82, 0xfe, 0, 0}
		binary.BigEndian.PutUint16(hdr[2:], uint16(len(payload)))
	default:
		return nil
	}
	hdr = append(hdr, 0, 0, 0, 0)
	return append(hdr, payload...)
}
