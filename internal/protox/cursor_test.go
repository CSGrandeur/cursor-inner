package protox

import (
	"bytes"
	"testing"
)

func bidiBody(client []byte, requestID string) []byte {
	body := AppendString(nil, 1, EncodeHex(client))
	return AppendBytes(body, 2, AppendString(nil, 1, requestID))
}

func TestDecodeBidiAndRunID(t *testing.T) {
	client := AppendBytes(nil, 1, AppendString(nil, 5, "conv"))
	id, got, err := DecodeBidi(Frame(0, bidiBody(client, "req-1")))
	if err != nil || id != "req-1" || !bytes.Equal(got, client) {
		t.Fatalf("id=%q err=%v", id, err)
	}
	runID, err := DecodeRunID(Frame(0, AppendString(nil, 1, "req-1")))
	if err != nil || runID != "req-1" {
		t.Fatal(runID, err)
	}
}

func TestPlainUnwrapsGzipBidi(t *testing.T) {
	client := AppendBytes(nil, 1, AppendString(nil, 5, "conv"))
	bidi := bidiBody(client, "req-gz")
	cases := map[string]struct {
		body     []byte
		encoding string
	}{
		"http gzip":        {gzipBytes(t, bidi), "gzip"},
		"compressed frame": {Frame(0x01, gzipBytes(t, bidi)), ""},
	}
	for name, tc := range cases {
		plain, err := Plain(tc.body, tc.encoding)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		id, got, err := DecodeBidi(plain)
		if err != nil || id != "req-gz" || !bytes.Equal(got, client) {
			t.Fatalf("%s: id=%q err=%v", name, id, err)
		}
	}
}
