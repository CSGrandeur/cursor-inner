package grokbot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindGrokUsesFirstExisting(t *testing.T) {
	dir := t.TempDir()
	hit := filepath.Join(dir, "Grok Bot.exe")
	if err := os.WriteFile(hit, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findGrok([]string{filepath.Join(dir, "missing.exe"), hit}, []func() (string, error){
		func() (string, error) {
			t.Fatal("lookup should not run")
			return "", nil
		},
	})
	if err != nil || got != hit {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestFindGrokResolvesInstallDirAndLookup(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "Grok Bot.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findGrok(nil, []func() (string, error){func() (string, error) { return root, nil }})
	if err != nil || got != exe {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestFindGrokReportsMissing(t *testing.T) {
	if _, err := findGrok(nil, []func() (string, error){func() (string, error) { return "", os.ErrNotExist }}); err == nil {
		t.Fatal("expected error")
	}
}

func TestGrokExecutableStripsDisplayIconSuffix(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "Grok Bot.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := grokExecutable(`"` + exe + `",0`)
	if got != exe {
		t.Fatalf("got %q", got)
	}
}

func TestExeFromUninstallIgnoresOtherApps(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "Grok Bot.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := exeFromUninstall("Other App", dir, exe); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := exeFromUninstall("Grok Bot", dir, ""); got != exe {
		t.Fatalf("got %q", got)
	}
}
