//go:build !windows

package procfwd

import "os"

func replaceExisting(tmp, path string) error {
	return os.Rename(tmp, path)
}
