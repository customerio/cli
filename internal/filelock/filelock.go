package filelock

import (
	"errors"
	"fmt"
	"os"
)

// ErrLocked reports that another process holds the lock. TryLock returns it
// instead of waiting, so the caller decides what a held lock means for it.
var ErrLocked = errors.New("filelock: held by another process")

// Lock acquires an exclusive lock on path and returns an unlock function.
//
// It blocks until the lock is free. There is no timeout — the underlying
// primitive has none, and a portable one cannot be layered on top — so a
// caller that cannot afford an open-ended wait must use TryLock instead.
func Lock(path string, mode os.FileMode) (func(), error) {
	return acquire(path, mode, lockFile)
}

// TryLock acquires an exclusive lock on path without waiting. When another
// process holds the lock it returns ErrLocked at once; any other failure is
// reported as it is for Lock.
func TryLock(path string, mode os.FileMode) (func(), error) {
	return acquire(path, mode, tryLockFile)
}

func acquire(path string, mode os.FileMode, lock func(*os.File) error) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, mode)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := lock(f); err != nil {
		f.Close()
		if errors.Is(err, ErrLocked) {
			return nil, err
		}
		return nil, fmt.Errorf("acquire lock: %w", err)
	}

	return func() {
		_ = unlockFile(f)
		_ = f.Close()
	}, nil
}
