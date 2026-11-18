package antigravitylaunch

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindAntigravityUsesFirstExisting(t *testing.T) {
	dir := t.TempDir()
	hit := filepath.Join(dir, "Antigravity.exe")
	if err := os.WriteFile(hit, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findAntigravity([]string{filepath.Join(dir, "missing.exe"), hit}, func() (string, error) {
		t.Fatal("lookup should not run")
		return "", nil
	})
	if err != nil || got != hit {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestFindAntigravityResolvesBinShim(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "Antigravity.exe")
	shim := filepath.Join(root, "resources", "app", "bin", "antigravity.cmd")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findAntigravity(nil, func() (string, error) { return shim, nil })
	if err != nil || got != exe {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestFindAntigravityReportsMissing(t *testing.T) {
	if _, err := findAntigravity(nil, func() (string, error) { return "", os.ErrNotExist }); err == nil {
		t.Fatal("expected error")
	}
}

func TestExeFromUninstallResolvesIconAndDir(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "Antigravity.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := exeFromUninstall("Antigravity 2.31.2", "", `"`+exe+`",0`); got != exe {
		t.Fatalf("from icon got %q want %q", got, exe)
	}
	if got := exeFromUninstall("Antigravity 2.31.2", dir, ""); got != exe {
		t.Fatalf("from dir got %q want %q", got, exe)
	}
	if got := exeFromUninstall("Other App", dir, exe); got != "" {
		t.Fatalf("unexpected match for other app: %q", got)
	}
}

func TestResourcesDirsLayoutsAndSymlink(t *testing.T) {
	t.Run("windows layout", func(t *testing.T) {
		root := t.TempDir()
		exe := filepath.Join(root, "Antigravity.exe")
		res := filepath.Join(root, "resources")
		if err := os.MkdirAll(res, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := uniqueExistingDirs(resourcesDirsForExe(exe))
		if len(got) != 1 || got[0] != res {
			t.Fatalf("got %v, want [%s]", got, res)
		}
	})

	t.Run("darwin app bundle", func(t *testing.T) {
		root := t.TempDir()
		macOSDir := filepath.Join(root, "Antigravity.app", "Contents", "MacOS")
		res := filepath.Join(root, "Antigravity.app", "Contents", "Resources")
		if err := os.MkdirAll(macOSDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(res, 0o755); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(macOSDir, "Antigravity")
		if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		got := uniqueExistingDirs(resourcesDirsForExe(bin))
		if len(got) != 1 || got[0] != res {
			t.Fatalf("got %v, want [%s]", got, res)
		}
	})

	t.Run("linux symlink and shim", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX symlink test")
		}
		root := t.TempDir()
		optBin := filepath.Join(root, "opt", "Antigravity", "antigravity")
		res := filepath.Join(root, "opt", "Antigravity", "resources")
		shimDir := filepath.Join(root, "usr", "bin")
		shim := filepath.Join(shimDir, "antigravity")
		if err := os.MkdirAll(res, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(shimDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(optBin, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(optBin, shim); err != nil {
			t.Fatal(err)
		}
		got := ResourcesDirs(shim)
		found := false
		for _, d := range got {
			if d == res {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ResourcesDirs(%s) = %v, missing symlink target %s", shim, got, res)
		}
	})
}
