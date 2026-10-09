//go:build darwin || linux

package program

import "testing"

func TestSecondInstanceIsRefused(t *testing.T) {
	dir := t.TempDir()
	release, already, err := acquireSingleton(dir)
	if err != nil || already {
		t.Fatalf("first: already=%v err=%v", already, err)
	}
	if _, already, err := acquireSingleton(dir); err != nil || !already {
		t.Fatalf("second: already=%v err=%v", already, err)
	}
	release()
	release2, already, err := acquireSingleton(dir)
	if err != nil || already {
		t.Fatalf("after release: already=%v err=%v", already, err)
	}
	release2()
}
