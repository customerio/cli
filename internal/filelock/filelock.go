package filelock

import (
	"fmt"
	"os"
)

// Lock acquires an exclusive lock on path and returns an unlock function.
func Lock(path string, mode os.FileMode) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, mode)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire lock: %w", err)
	}

	return func() {
		_ = unlockFile(f)
		_ = f.Close()
	}, nil
}
