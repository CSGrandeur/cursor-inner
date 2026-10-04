package takeover

import (
	"os"
	"path/filepath"

	"cursor-inner/internal/fsutil"
)

func markPath(dir string) string {
	return filepath.Join(dir, "takeover.on")
}

func Mark(dir string) error {
	return fsutil.WriteFile(markPath(dir), []byte("1\n"))
}

func Marked(dir string) bool {
	_, err := os.Stat(markPath(dir))
	return err == nil
}

func Unmark(dir string) {
	_ = os.Remove(markPath(dir))
}
