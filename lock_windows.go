//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// lockSync serializes syncs on Windows using exclusive file creation. Windows
// has no flock; O_EXCL on a lock file gives the same "second run backs off"
// behavior. A stale lock left by a killed process is cleared on the next run
// once it is older than one hour — a sync never legitimately takes that long.
func lockSync() func() {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		fatal("create state dir: %v", err)
	}
	path := filepath.Join(stateDir(), "sync.lock")
	if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > time.Hour {
		os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err != nil {
		fmt.Println("another sync is already running; skipping")
		os.Exit(0)
	}
	return func() {
		f.Close()
		os.Remove(path)
	}
}
