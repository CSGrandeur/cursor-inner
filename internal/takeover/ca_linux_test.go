//go:build linux

package takeover

import (
	"encoding/pem"
	"testing"
)

func TestPEMContainsFindsLeafInBundle(t *testing.T) {
	cert, err := EnsureCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other, err := EnsureCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.Leaf.Raw}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Leaf.Raw})...)
	if !pemContains(bundle, cert.Leaf) {
		t.Fatal("leaf not found in bundle")
	}
	if pemContains(bundle[:len(bundle)/2], cert.Leaf) {
		t.Fatal("found leaf in bundle without it")
	}
}

func TestShellQuoteKeepsPathLiteral(t *testing.T) {
	if got := shellQuote(`/home/a b/$x"y`); got != `"/home/a b/\$x\"y"` {
		t.Fatal(got)
	}
}
