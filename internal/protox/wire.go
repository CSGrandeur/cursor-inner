package protox

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

var errTrunc = errors.New("protobuf 被截断")

type Field struct {
	Num  int
	Wire int
	Val  uint64
	Raw  []byte
}

func Fields(b []byte) ([]Field, error) {
	var out []Field
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errTrunc
		}
		b = b[n:]
		f := Field{Num: int(tag >> 3), Wire: int(tag & 7)}
		switch f.Wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errTrunc
			}
			f.Val = v
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return nil, errTrunc
			}
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errTrunc
			}
			b = b[n:]
			if uint64(len(b)) < l {
				return nil, errTrunc
			}
			f.Raw = append([]byte(nil), b[:l]...)
			b = b[l:]
		case 5:
			if len(b) < 4 {
				return nil, errTrunc
			}
			b = b[4:]
		default:
			return nil, fmt.Errorf("不支持的 protobuf wire %d", f.Wire)
		}
		out = append(out, f)
	}
	return out, nil
}

func Child(b []byte, num int) []byte {
	fields, err := Fields(b)
	if err != nil {
		return nil
	}
	for _, f := range fields {
		if f.Num == num && f.Wire == 2 {
			return f.Raw
		}
	}
	return nil
}

func Children(b []byte, num int) [][]byte {
	fields, err := Fields(b)
	if err != nil {
		return nil
	}
	var out [][]byte
	for _, f := range fields {
		if f.Num == num && f.Wire == 2 {
			out = append(out, f.Raw)
		}
	}
	return out
}

func StringField(b []byte, num int) string {
	raw := Child(b, num)
	if raw == nil {
		return ""
	}
	return string(raw)
}

func AppendString(dst []byte, num int, s string) []byte {
	dst = appendKey(dst, num, 2)
	dst = binary.AppendUvarint(dst, uint64(len(s)))
	return append(dst, s...)
}

func AppendBytes(dst []byte, num int, b []byte) []byte {
	dst = appendKey(dst, num, 2)
	dst = binary.AppendUvarint(dst, uint64(len(b)))
	return append(dst, b...)
}

func AppendVarint(dst []byte, num int, v uint64) []byte {
	dst = appendKey(dst, num, 0)
	return binary.AppendUvarint(dst, v)
}

func AppendBool(dst []byte, num int, v bool) []byte {
	if !v {
		return dst
	}
	dst = appendKey(dst, num, 0)
	return append(dst, 1)
}

func appendKey(dst []byte, num, wire int) []byte {
	return binary.AppendUvarint(dst, uint64(num<<3|wire))
}

func Frame(flags byte, payload []byte) []byte {
	out := make([]byte, 5+len(payload))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

func Unary(body []byte) (payload []byte, framed bool) {
	if len(body) >= 5 {
		n := binary.BigEndian.Uint32(body[1:5])
		if body[0]&0x02 == 0 && int(n) == len(body)-5 {
			return body[5:], true
		}
	}
	return body, false
}

func Plain(body []byte, contentEncoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "", "identity":
	case "gzip", "x-gzip":
		decoded, err := gunzip(body)
		if err != nil {
			return nil, err
		}
		body = decoded
	default:
		return nil, fmt.Errorf("不支持的请求压缩 %s", contentEncoding)
	}
	if len(body) >= 5 && body[0]&0x01 != 0 && body[0]&0x02 == 0 {
		n := binary.BigEndian.Uint32(body[1:5])
		if int(n) == len(body)-5 {
			return gunzip(body[5:])
		}
	}
	return body, nil
}

func MergeFramed(body, extra []byte) []byte {
	payload, framed := Unary(body)
	merged := append(append([]byte{}, payload...), extra...)
	if framed {
		return Frame(body[0], merged)
	}
	return merged
}

// MergePlain 只在未压缩的 Connect 帧上追加字段。压缩帧或长度对不上时返回 false，调用方应原样转发。
func MergePlain(body, extra []byte) ([]byte, bool) {
	if len(extra) == 0 {
		return append([]byte{}, body...), true
	}
	frames, ok := splitFrames(body)
	if !ok {
		return append(append([]byte{}, body...), extra...), true
	}
	out := make([]byte, 0, len(body)+len(extra)+5)
	merged := false
	for _, frame := range frames {
		payload := frame.payload
		flags := frame.flags
		if !merged && flags&0x02 == 0 {
			if flags&0x01 != 0 {
				decoded, err := gunzip(payload)
				if err != nil {
					return nil, false
				}
				payload = decoded
			}
			payload = append(append([]byte{}, payload...), extra...)
			flags = 0
			merged = true
		}
		out = append(out, Frame(flags, payload)...)
	}
	if !merged {
		return append(Frame(0, extra), out...), true
	}
	return out, true
}

type connFrame struct {
	flags   byte
	payload []byte
}

func splitFrames(body []byte) ([]connFrame, bool) {
	if len(body) < 5 {
		return nil, false
	}
	var frames []connFrame
	rest := body
	for len(rest) >= 5 {
		n := int(binary.BigEndian.Uint32(rest[1:5]))
		if n < 0 || 5+n > len(rest) {
			return nil, false
		}
		frames = append(frames, connFrame{flags: rest[0], payload: rest[5 : 5+n]})
		rest = rest[5+n:]
	}
	if len(rest) != 0 {
		return nil, false
	}
	return frames, true
}

func gunzip(raw []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, 32<<20))
}
