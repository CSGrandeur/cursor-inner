package procfwd

import (
	"strings"
	"testing"
)

func TestApplyBlock(t *testing.T) {
	got := ApplyBlock("# keep\n1.2.3.4 example.test\n")
	if !strings.HasPrefix(got, markerBegin+"\n") || !strings.Contains(got, "127.0.0.1 api3.cursor.sh\n") {
		t.Fatalf("block missing: %q", got)
	}
	if !strings.Contains(got, "1.2.3.4 example.test\n") {
		t.Fatalf("original line dropped: %q", got)
	}
	if strings.Contains(got, "api2.cursor.sh") || strings.Contains(got, "api5.cursor.sh") {
		t.Fatalf("decrypt hosts were included: %q", got)
	}
	again := ApplyBlock(got)
	if again != got {
		t.Fatalf("apply not idempotent:\n%s\n%s", got, again)
	}
	if RemoveBlock(got) != "# keep\n1.2.3.4 example.test\n" {
		t.Fatalf("remove: %q", RemoveBlock(got))
	}
}

func TestRemoveBlockAbsent(t *testing.T) {
	in := "127.0.0.1 localhost\n"
	if RemoveBlock(in) != in {
		t.Fatalf("changed unrelated hosts: %q", RemoveBlock(in))
	}
}
