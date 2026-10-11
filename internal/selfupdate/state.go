package selfupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrRollback 表示更新源给出的版本比之前验过的旧：可能是有人重放旧的签名清单，想让用户停在旧版本。
var ErrRollback = errors.New("更新源返回的版本比之前见过的旧，已忽略")

// trustState 记录验签通过的清单里见过的最高版本（data/update/trust.json），用来识别回滚和冻结：
// 签名只能证明清单是我们发的，不能证明它是最新的。
type trustState struct {
	Highest string    `json:"highest"`
	SeenAt  time.Time `json:"seen_at"`
}

func trustPath(dataDir string) string { return filepath.Join(dataDir, "update", "trust.json") }

func loadTrust(dataDir string) trustState {
	var st trustState
	if dataDir == "" {
		return st
	}
	if err := readJSON(trustPath(dataDir), &st); err != nil {
		return trustState{}
	}
	if _, ok := ParseVersion(st.Highest); !ok {
		return trustState{}
	}
	return st
}

// noteSeen 把验过的版本记下来（只升不降）。
func noteSeen(dataDir, version string, now time.Time) {
	if dataDir == "" {
		return
	}
	v, ok := ParseVersion(version)
	if !ok {
		return
	}
	st := loadTrust(dataDir)
	if old, ok := ParseVersion(st.Highest); ok && Compare(v, old) <= 0 {
		return
	}
	_ = os.MkdirAll(filepath.Dir(trustPath(dataDir)), 0o755)
	_ = writeJSON(trustPath(dataDir), trustState{Highest: v.String(), SeenAt: now.UTC()})
}

// checkRollback：清单版本比见过的最高版本旧，并且那个更高的版本比当前还新（也就是用户还没装上它）时，
// 视为回滚。比当前旧的清单本来就不会触发更新，不算错误。
func checkRollback(dataDir, current, manifest string) error {
	st := loadTrust(dataDir)
	hi, ok := ParseVersion(st.Highest)
	if !ok {
		return nil
	}
	mv, _ := ParseVersion(manifest)
	if Compare(mv, hi) >= 0 {
		return nil
	}
	if cv, ok := ParseVersion(current); ok && Compare(cv, hi) >= 0 {
		return nil
	}
	return fmt.Errorf("%w（这次是 %s，之前见过 %s）", ErrRollback, mv, hi)
}
