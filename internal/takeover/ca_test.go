package takeover

import "testing"

func TestEnsureCAIsStable(t *testing.T) {
	dir := t.TempDir()
	first, err := EnsureCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Leaf.Subject.CommonName != "cursor-inner Local CA" {
		t.Fatal(first.Leaf.Subject.CommonName)
	}
	if first.Leaf.SerialNumber.Cmp(second.Leaf.SerialNumber) != 0 {
		t.Fatal("regenerated")
	}
}
