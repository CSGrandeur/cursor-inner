//go:build !windows

package antigravitylaunch

func Close(string) (bool, error) { return false, nil }

func ManagedRunning(string) bool { return false }
