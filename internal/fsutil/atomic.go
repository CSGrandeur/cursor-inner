package fsutil

import (
	"os"
	"path/filepath"
)

func WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil {
		os.Remove(name)
		return werr
	}
	if cerr != nil {
		os.Remove(name)
		return cerr
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(path)
		if err2 := os.Rename(name, path); err2 != nil {
			os.Remove(name)
			return err
		}
	}
	return nil
}
