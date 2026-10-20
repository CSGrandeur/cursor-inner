package selfupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSwapAndRevert(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "cursor-inner.exe")
	staged := filepath.Join(dir, "update", "staged.exe")
	os.MkdirAll(filepath.Dir(staged), 0o755)
	os.WriteFile(exe, []byte("old"), 0o755)
	os.WriteFile(staged, []byte("new"), 0o755)
	backup, err := Swap(exe, staged)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("exe=%q", b)
	}
	if b, _ := os.ReadFile(backup); string(b) != "old" {
		t.Fatalf("backup=%q", b)
	}
	if err := Revert(exe, backup); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("after revert exe=%q", b)
	}
	if b, _ := os.ReadFile(exe + ".failed"); string(b) != "new" {
		t.Fatalf("failed copy=%q", b)
	}
	CleanupOld(exe)
	if _, err := os.Stat(exe + ".failed"); !os.IsNotExist(err) {
		t.Fatal("cleanup left .failed")
	}
}

func TestSwapMissingStagedKeepsOld(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "cursor-inner.exe")
	os.WriteFile(exe, []byte("old"), 0o755)
	if _, err := Swap(exe, filepath.Join(dir, "nope")); err == nil {
		t.Fatal("expected error")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("exe=%q", b)
	}
}

// 从下载目录直接运行、文件随后被清理掉时，路径上已经没有可备份的旧版本。
// 只要目录还在，就把新版本放到原路径继续更新，不能卡在「备份当前程序失败」。
func TestSwapWhenCurrentExeIsGone(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "cursor-inner.exe")
	staged := filepath.Join(dir, "staged.exe")
	os.WriteFile(staged, []byte("new"), 0o755)
	backup, err := Swap(exe, staged)
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Fatalf("backup=%q", backup)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("exe=%q", b)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatal("staged was not moved")
	}
	if err := Revert(exe, backup); err != nil {
		t.Fatalf("revert without a backup: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("revert changed the new file: %q", b)
	}
}

func TestSwapWhenDirectoryIsGone(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "gone", "cursor-inner.exe")
	staged := filepath.Join(dir, "staged.exe")
	os.WriteFile(staged, []byte("new"), 0o755)
	if _, err := Swap(exe, staged); err == nil || !strings.Contains(err.Error(), "目录") {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatal("staged should be left in place")
	}
}
