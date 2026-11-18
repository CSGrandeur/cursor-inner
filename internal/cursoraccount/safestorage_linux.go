//go:build !windows && !darwin

package cursoraccount

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

func decryptSafeStorageBlob(_ string, blob []byte) (string, error) {
	if len(blob) < 3 {
		return "", errors.New("safeStorage blob too short")
	}
	ver := string(blob[:3])
	pass := "peanuts"
	if ver == "v11" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, attr := range [][]string{{"application", "Cursor"}, {"service", "Cursor Safe Storage"}} {
			if out, err := exec.CommandContext(ctx, "secret-tool", append([]string{"lookup"}, attr...)...).Output(); err == nil {
				if p := strings.TrimSpace(string(out)); p != "" {
					pass = p
					break
				}
			}
		}
	} else if ver != "v10" {
		return "", errors.New("unexpected Linux safeStorage header")
	}
	return decryptAES128CBC(pass, 1, blob)
}

func encodePlatformToken(_, plain string) string { return plain }
