package mitm

import (
	"bytes"
	"testing"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/protox"
)

func TestLeakedModel(t *testing.T) {
	entries := []catalog.Entry{{ID: "model-12345678", DisplayName: "Mine"}}
	if got := leakedModel([]byte(`{"model":"model-12345678"}`), entries); got != "model-12345678" {
		t.Fatal(got)
	}
	if got := leakedModel([]byte(`{"model":"claude"}`), entries); got != "" {
		t.Fatal(got)
	}
	if got := leakedModel([]byte("short"), []catalog.Entry{{ID: "ab"}}); got != "" {
		t.Fatal(got)
	}
}

func TestScrubCustomIDsKeepsLength(t *testing.T) {
	const id = "abcdef0123456789"
	entries := []catalog.Entry{{ID: id}}
	body := []byte("before-" + id + "-after")
	got := scrubCustomIDs(body, entries)
	if bytes.Contains(got, []byte(id)) {
		t.Fatalf("id still present: %q", got)
	}
	if len(got) != len(body) {
		t.Fatalf("length changed: %d -> %d", len(body), len(got))
	}
	if !bytes.Contains(got, bytes.Repeat([]byte{'0'}, len(id))) {
		t.Fatalf("missing placeholder: %q", got)
	}
}

func TestScrubOutboundConnectFrame(t *testing.T) {
	const id = "abcdef0123456789"
	entries := []catalog.Entry{{ID: id}}
	plain := protox.AppendString(nil, 1, id)
	framed := protox.Frame(0, plain)
	next, enc, ok := scrubOutbound(framed, "", entries)
	if !ok || enc != "" {
		t.Fatalf("ok=%v enc=%q", ok, enc)
	}
	if leakedModel(next, entries) != "" {
		t.Fatalf("still leaking: %q", next)
	}
	outPlain, err := protox.Plain(next, "")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(outPlain, []byte(id)) {
		t.Fatalf("plain still has id: %q", outPlain)
	}
}

func TestScrubOutboundRemembersNothingAboutOfficial(t *testing.T) {
	entries := []catalog.Entry{{ID: "abcdef0123456789"}}
	body := []byte("official-model-only")
	next, enc, ok := scrubOutbound(body, "", entries)
	if ok || enc != "" || !bytes.Equal(next, body) {
		t.Fatalf("ok=%v enc=%q next=%q", ok, enc, next)
	}
}

func TestQuietLeakPaths(t *testing.T) {
	for _, path := range []string{
		"/aiserver.v1.AnalyticsService/Batch",
		"/aiserver.v1.AiService/GetDefaultModelNudgeData",
		"/aiserver.v1.AiService/GetNewChatNudge",
		"/aiserver.v1.AiService/NameTab",
		"/aiserver.v1.DashboardService/GetUsageLimitStatusAndActiveGrants",
	} {
		if !quietLeak(path) {
			t.Fatal(path)
		}
	}
	if quietLeak("/aiserver.v1.BidiService/BidiAppend") {
		t.Fatal("agent path should not scrub")
	}
}
