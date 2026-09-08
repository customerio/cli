//go:build windows

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lockFile(f *os.File) error {
	return lockFileEx(f, windows.LOCKFILE_EXCLUSIVE_LOCK)
}

// tryLockFile is lockFile with LOCKFILE_FAIL_IMMEDIATELY: LockFileEx returns
// ERROR_LOCK_VIOLATION instead of waiting when the lock is held elsewhere.
func tryLockFile(f *os.File) error {
	err := lockFileEx(f, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}
	return err
}

func lockFileEx(f *os.File, flags uint32) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		flags,
		0,
		1,
		0,
		&overlapped,
	)
}

func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		1,
		0,
		&overlapped,
	)
}
