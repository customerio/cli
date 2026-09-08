package filelock

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestTryLock_AcquiresWhenFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	unlock, err := TryLock(path, 0600)
	if err != nil {
		t.Fatalf("TryLock() on a free lock: %v", err)
	}
	unlock()
}

// The whole point of TryLock: a held lock is reported, not waited for. Both
// flock and LockFileEx conflict across separate opens of the same file within
// one process, so holding via Lock and probing via TryLock is a real contest.
func TestTryLock_HeldReturnsErrLockedWithoutWaiting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	unlock, err := Lock(path, 0600)
	if err != nil {
		t.Fatalf("Lock(): %v", err)
	}
	defer unlock()

	start := time.Now()
	_, err = TryLock(path, 0600)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrLocked) {
		t.Fatalf("TryLock() on a held lock: want ErrLocked, got %v", err)
	}
	// Lock would sit here until the holder released; TryLock must not. The
	// bound is generous so a slow CI runner cannot turn it into a flake — any
	// blocking implementation would exceed it by orders of magnitude.
	if elapsed > 2*time.Second {
		t.Errorf("TryLock() took %v on a held lock; it must return at once", elapsed)
	}
}

func TestTryLock_SucceedsOnceReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	unlock, err := Lock(path, 0600)
	if err != nil {
		t.Fatalf("Lock(): %v", err)
	}
	if _, err := TryLock(path, 0600); !errors.Is(err, ErrLocked) {
		t.Fatalf("expected ErrLocked while held, got %v", err)
	}
	unlock()

	unlock, err = TryLock(path, 0600)
	if err != nil {
		t.Fatalf("TryLock() after release: %v", err)
	}
	unlock()
}

// Lock's error contract is unchanged: it still blocks, and still succeeds once
// the holder lets go — TryLock is an addition, not a change to Lock.
func TestLock_StillBlocksThenAcquires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	unlock, err := Lock(path, 0600)
	if err != nil {
		t.Fatalf("Lock(): %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		u, err := Lock(path, 0600)
		if err != nil {
			t.Errorf("second Lock(): %v", err)
			close(acquired)
			return
		}
		u()
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("second Lock() returned while the first was still held")
	case <-time.After(200 * time.Millisecond):
		// Still waiting, as it should be.
	}

	unlock()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("second Lock() did not acquire after the first was released")
	}
}
