package filelock

import (
	"path/filepath"
	"testing"
)

func TestLockReleaseAndRelock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	unlock, err := Lock(path, 0600)
	if err != nil {
		t.Fatalf("Lock() error = %v", err)
	}
	unlock()

	unlock, err = Lock(path, 0600)
	if err != nil {
		t.Fatalf("Lock() after unlock error = %v", err)
	}
	unlock()
}
