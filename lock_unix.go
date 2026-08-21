//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockSync serializes syncs. The daemon timer and the session-start hook both
// fire sync-all, and two runs sharing one clone can hash a half-checked-out
// tree or lose a state.json write. A second run exits rather than queueing:
// the next tick is at most 15 minutes away.
func lockSync() func() {
	f := openLockFile()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Println("another sync is already running; skipping")
		f.Close()
		os.Exit(0)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

func openLockFile() *os.File {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		fatal("create state dir: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(stateDir(), "sync.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fatal("open lock: %v", err)
	}
	return f
}
