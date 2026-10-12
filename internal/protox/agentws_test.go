package protox

import "testing"

func TestClassifyAgentRunStartCarriesBothIDs(t *testing.T) {
	ident := AppendString(nil, 1, "run-1")
	ident = AppendString(ident, 2, "req-1")
	frame := AppendBytes(nil, 2, AppendBytes(nil, 1, ident))
	got := ClassifyAgentPayload(frame)
	if got.Kind != "meta" || got.RunID != "run-1" || got.RequestID != "req-1" {
		t.Fatalf("meta %+v", got)
	}
	accepted := EncodeAgentRunAccepted(got.RunID, got.RequestID)
	if StringField(Child(accepted, 2), 1) == "" || StringField(Child(Child(accepted, 2), 1), 1) != "run-1" || StringField(Child(Child(accepted, 2), 1), 2) != "req-1" {
		t.Fatalf("accepted %x", accepted)
	}
	if string(EncodeAgentMetaAck("same")) != string(EncodeAgentRunAccepted("same", "same")) {
		t.Fatal("old ack bytes changed")
	}
}
