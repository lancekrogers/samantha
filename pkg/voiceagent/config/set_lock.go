package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	// configLockStale is how long a lock file may sit before a later writer
	// treats it as abandoned. A write is a parse + patch + rename measured in
	// milliseconds, so five seconds only ever breaks a crashed writer's lock.
	configLockStale = 5 * time.Second
	// keptBackups bounds the timestamped .bak files, which otherwise grow
	// forever — one per write, for the life of the install.
	keptBackups = 5
)

// acquireConfigLock takes an advisory lock on the config file and returns the
// release function. The TUI, the CLI and the Mac app are three writers of one
// file; without this, two read-modify-writes interleave and one set silently
// disappears.
func acquireConfigLock(path string) (func(), error) {
	lockPath := path + ".lock"
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(file, "%d\n", os.Getpid())
			_ = file.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !os.IsExist(err) {
			return nil, writeFailed("", "creating config lock", err)
		}
		info, statErr := os.Stat(lockPath)
		if statErr != nil || time.Since(info.ModTime()) <= configLockStale {
			break
		}
		if removeErr := os.Remove(lockPath); removeErr != nil {
			break
		}
	}
	return nil, &SetError{
		Code:    CodeLocked,
		Message: "another writer is saving settings right now; try again",
	}
}

// pruneBackups keeps the newest keep backups of path and removes the rest. The
// backup name embeds a zero-padded UTC timestamp, so lexical order is
// chronological order.
func pruneBackups(path string, keep int) {
	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil || len(matches) <= keep {
		return
	}
	sort.Strings(matches)
	for _, stale := range matches[:len(matches)-keep] {
		_ = os.Remove(stale)
	}
}
