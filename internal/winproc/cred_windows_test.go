//go:build windows

package winproc

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

// 真实调用系统凭据库：写入、读回、删除一条测试专用凭据，不碰任何现有条目。
func TestCredRoundTrip(t *testing.T) {
	target := fmt.Sprintf("cursor-inner-test-%d", os.Getpid())
	_ = CredDelete(target)
	t.Cleanup(func() { _ = CredDelete(target) })

	blob := []byte(`{"token":{"access_token":"a"}}`)
	if err := CredWrite(target, "cursor-inner-test", blob); err != nil {
		t.Fatalf("CredWrite: %v", err)
	}
	got, err := CredRead(target)
	if err != nil {
		t.Fatalf("CredRead: %v", err)
	}
	if string(got) != string(blob) {
		t.Fatalf("blob mismatch: %q", got)
	}
	if err := CredWrite(target, "cursor-inner-test", []byte("second")); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got, err = CredRead(target); err != nil || string(got) != "second" {
		t.Fatalf("overwrite read: %q %v", got, err)
	}
	if err := CredDelete(target); err != nil {
		t.Fatalf("CredDelete: %v", err)
	}
	if _, err := CredRead(target); !errors.Is(err, syscall.ERROR_NOT_FOUND) {
		t.Fatalf("read after delete: %v", err)
	}
	if err := CredDelete(target); !errors.Is(err, syscall.ERROR_NOT_FOUND) {
		t.Fatalf("delete again: %v", err)
	}
}
