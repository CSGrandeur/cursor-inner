package protox

import "testing"

func TestBidiCarriesModelAndUserText(t *testing.T) {
	user := AppendString(nil, 1, "你好")
	userAction := AppendBytes(nil, 1, user)
	action := AppendBytes(nil, 1, userAction)
	requested := AppendString(nil, 1, "local-hash")
	run := AppendBytes(nil, 2, action)
	run = AppendBytes(run, 9, requested)
	client := AppendBytes(nil, 1, run)

	bidi := AppendString(nil, 1, EncodeHex(client))
	bidi = AppendBytes(bidi, 2, AppendString(nil, 1, "req-1"))
	id, got, err := DecodeBidi(Frame(0, bidi))
	if err != nil {
		t.Fatal(err)
	}
	if id != "req-1" || ModelID(got) != "local-hash" {
		t.Fatalf("id=%s model=%s", id, ModelID(got))
	}
	msgs := ChatMessages(got)
	if len(msgs) != 1 || msgs[0].Content != "你好" || msgs[0].Role != "user" {
		t.Fatalf("%+v", msgs)
	}
	runID, err := DecodeRunID(Frame(0, AppendString(nil, 1, "req-1")))
	if err != nil || runID != "req-1" {
		t.Fatal(runID, err)
	}
}

func TestPlainUnwrapsGzipBidi(t *testing.T) {
	requested := AppendString(nil, 1, "local-hash")
	run := AppendBytes(nil, 9, requested)
	client := AppendBytes(nil, 1, run)
	bidi := AppendString(nil, 1, EncodeHex(client))
	bidi = AppendBytes(bidi, 2, AppendString(nil, 1, "req-gz"))

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
		if err != nil || id != "req-gz" || ModelID(got) != "local-hash" {
			t.Fatalf("%s: id=%q model=%q err=%v", name, id, ModelID(got), err)
		}
	}
}

func TestTextDeltaShape(t *testing.T) {
	payload, framed := Unary(TextDelta("hi"))
	if !framed {
		t.Fatal("frame")
	}
	update := Child(payload, 1)
	delta := Child(update, 1)
	if StringField(delta, 1) != "hi" {
		t.Fatal(StringField(delta, 1))
	}
}
