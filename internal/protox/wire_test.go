package protox

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"testing"
)

func TestStringRoundTripAndMerge(t *testing.T) {
	msg := AppendString(nil, 1, "opus")
	msg = AppendBytes(msg, 2, AppendString(nil, 1, "gpt"))
	if StringField(msg, 1) != "opus" {
		t.Fatal(StringField(msg, 1))
	}
	if StringField(Child(msg, 2), 1) != "gpt" {
		t.Fatal("child")
	}
	framed := Frame(0, msg)
	extra := AppendString(nil, 1, "local")
	merged := MergeFramed(framed, extra)
	payload, ok := Unary(merged)
	if !ok {
		t.Fatal("frame")
	}
	names := Children(payload, 1)
	if len(names) != 2 || string(names[0]) != "opus" || string(names[1]) != "local" {
		t.Fatalf("%q", names)
	}
}

func TestMergePlainLeavesCompressedFrameUntouched(t *testing.T) {
	plain := Frame(0, AppendString(nil, 1, "opus"))
	merged, ok := MergePlain(plain, AppendString(nil, 1, "local"))
	if !ok {
		t.Fatal("plain")
	}
	payload, framed := Unary(merged)
	if !framed || string(Children(payload, 1)[0]) != "opus" {
		t.Fatal("merged")
	}
	compressed := Frame(0x01, gzipBytes(t, AppendString(nil, 1, "opus")))
	merged, ok = MergePlain(compressed, AppendString(nil, 1, "local"))
	if !ok {
		t.Fatal("gzip catalog should be decompressed then merged")
	}
	payload, framed = Unary(firstFrame(merged))
	if !framed {
		t.Fatal("frame")
	}
	names := Children(payload, 1)
	if len(names) != 2 || string(names[0]) != "opus" || string(names[1]) != "local" {
		t.Fatalf("%q", names)
	}
}

func TestMergePlainKeepsEndStreamTrailer(t *testing.T) {
	data := Frame(0, AppendString(nil, 1, "opus"))
	trailer := Frame(0x02, []byte("{}"))
	body := append(append([]byte{}, data...), trailer...)
	merged, ok := MergePlain(body, AppendString(nil, 1, "local"))
	if !ok {
		t.Fatal("trailer body should still merge")
	}
	payload, framed := Unary(firstFrame(merged))
	if !framed {
		t.Fatal("frame")
	}
	names := Children(payload, 1)
	if len(names) != 2 || string(names[1]) != "local" {
		t.Fatalf("%q", names)
	}
	frames := mustFrames(t, merged)
	if len(frames) != 2 || frames[1].flags != 0x02 {
		t.Fatalf("want data+trailer, got %+v", frames)
	}
}

type testFrame struct {
	flags   byte
	payload []byte
}

func firstFrame(body []byte) []byte {
	n := binary.BigEndian.Uint32(body[1:5])
	return body[:5+int(n)]
}

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustFrames(t *testing.T, body []byte) []testFrame {
	t.Helper()
	var frames []testFrame
	rest := body
	for len(rest) >= 5 {
		n := int(binary.BigEndian.Uint32(rest[1:5]))
		if n < 0 || 5+n > len(rest) {
			t.Fatalf("bad frame n=%d remaining=%d", n, len(rest))
		}
		frames = append(frames, testFrame{flags: rest[0], payload: rest[5 : 5+n]})
		rest = rest[5+n:]
	}
	if len(rest) != 0 {
		t.Fatalf("trailing %d bytes", len(rest))
	}
	return frames
}
