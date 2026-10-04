//go:build !windows

package main

func acquireSingleton() (func(), bool, error) {
	return func() {}, false, nil
}
