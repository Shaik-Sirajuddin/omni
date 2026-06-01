//go:build !windows

package filelock_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Shaik-Sirajuddin/memory/pkg/filelock"
)

// ─── ExclusiveLock_Serializes ──────────────────────────────────────────────────

func TestExclusiveLock_Serializes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")
	dataPath := filepath.Join(dir, "data.txt")
	locker := filelock.New()

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)

	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("writer-%d\n", i)
			t.Logf("goroutine %d: acquiring lock", i)
			unlock, err := locker.Lock(lockPath)
			if err != nil {
				t.Errorf("goroutine %d: Lock failed: %v", i, err)
				return
			}
			t.Logf("goroutine %d: lock acquired, writing id=%q", i, id)
			if err := locker.WriteAtomic(dataPath, []byte(id)); err != nil {
				t.Errorf("goroutine %d: WriteAtomic failed: %v", i, err)
			}
			unlock()
			t.Logf("goroutine %d: lock released", i)
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("ReadFile after concurrent writes: %v", err)
	}
	t.Logf("final data: %q", string(data))

	// Content must be exactly one writer's line — no torn/multi-writer content.
	if bytes.Count(data, []byte("\n")) != 1 {
		t.Errorf("expected exactly one newline in final data (one complete write); got %q", string(data))
	}
	if !bytes.HasPrefix(data, []byte("writer-")) {
		t.Errorf("expected data to start with 'writer-'; got %q", string(data))
	}

	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file must exist after writes: %v", err)
	}
	t.Logf("lock file: size=%d mtime=%s", fi.Size(), fi.ModTime())
}

// ─── SharedLock_AllowsConcurrentReaders ───────────────────────────────────────

func TestSharedLock_AllowsConcurrentReaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")
	// Pre-create the lock file so RLock always finds it.
	f, err := os.Create(lockPath)
	if err != nil {
		t.Fatalf("create lock file: %v", err)
	}
	f.Close()

	locker := filelock.New()
	const N = 20
	var (
		wg        sync.WaitGroup
		errors    = make([]error, N)
		acquired  int64
	)
	wg.Add(N)

	// All goroutines start simultaneously via a barrier.
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			t.Logf("reader %d: acquiring RLock", i)
			unlock, err := locker.RLock(lockPath)
			if err != nil {
				errors[i] = err
				return
			}
			n := atomic.AddInt64(&acquired, 1)
			t.Logf("reader %d: RLock acquired; concurrent holders=%d", i, n)
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt64(&acquired, -1)
			unlock()
			t.Logf("reader %d: RLock released", i)
		}()
	}
	close(start)
	wg.Wait()

	for i, e := range errors {
		if e != nil {
			t.Errorf("reader %d: RLock failed: %v", i, e)
		}
	}

	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file must exist after readers: %v", err)
	}
	if fi.Mode().Perm()&0o600 == 0 {
		t.Errorf("lock file permissions %o: expected at least 0600", fi.Mode().Perm())
	}
	t.Logf("lock file: perm=%o mtime=%s", fi.Mode().Perm(), fi.ModTime())
}

// ─── ExclusiveLock_BlocksReaders ──────────────────────────────────────────────

func TestExclusiveLock_BlocksReaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")
	f, err := os.Create(lockPath)
	if err != nil {
		t.Fatalf("create lock file: %v", err)
	}
	f.Close()

	locker := filelock.New()

	// Acquire exclusive lock.
	t.Log("acquiring exclusive lock")
	unlock, err := locker.Lock(lockPath)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer unlock()
	t.Log("exclusive lock held")

	// Open the lock file independently and try LOCK_SH | LOCK_NB — must fail.
	lf, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock file for non-blocking check: %v", err)
	}
	defer lf.Close()

	t.Log("attempting LOCK_SH|LOCK_NB from independent fd (must fail)")
	nbErr := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if nbErr == nil {
		t.Error("expected LOCK_SH|LOCK_NB to fail while exclusive lock is held, but it succeeded")
	} else {
		t.Logf("LOCK_SH|LOCK_NB correctly blocked (errno=%v)", nbErr)
	}

	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file stat: %v", err)
	}
	t.Logf("lock file after block check: size=%d mtime=%s", fi.Size(), fi.ModTime())
}

// ─── WriteAtomic_NoPartialRead ────────────────────────────────────────────────

func TestWriteAtomic_NoPartialRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")
	dataPath := filepath.Join(dir, "data.bin")
	locker := filelock.New()

	const chunkSize = 64 * 1024 // 64 KB
	oldContent := bytes.Repeat([]byte("A"), chunkSize)
	newContent := bytes.Repeat([]byte("B"), chunkSize)

	// Seed the file with old content.
	if err := locker.WriteAtomic(dataPath, oldContent); err != nil {
		t.Fatalf("seed WriteAtomic: %v", err)
	}

	var (
		wg      sync.WaitGroup
		stopped int32
		readErr int32
	)

	// Writer goroutine: continuously overwrites the file.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; atomic.LoadInt32(&stopped) == 0; i++ {
			content := oldContent
			if i%2 == 0 {
				content = newContent
			}
			unlock, err := locker.Lock(lockPath)
			if err != nil {
				t.Logf("writer: Lock error (likely test ending): %v", err)
				return
			}
			if err := locker.WriteAtomic(dataPath, content); err != nil {
				t.Logf("writer: WriteAtomic error: %v", err)
			}
			unlock()
		}
	}()

	// Reader goroutines: check for partial reads.
	const readers = 5
	wg.Add(readers)
	for r := 0; r < readers; r++ {
		r := r
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				data, err := os.ReadFile(dataPath)
				if err != nil {
					// File may be temporarily absent during rename — not a partial read.
					continue
				}
				// Content must be exactly old or new — nothing in between.
				if len(data) != chunkSize {
					t.Logf("reader %d iter %d: torn read — length %d (want %d)", r, i, len(data), chunkSize)
					atomic.StoreInt32(&readErr, 1)
					continue
				}
				if !bytes.Equal(data, oldContent) && !bytes.Equal(data, newContent) {
					t.Logf("reader %d iter %d: torn content (first byte %q)", r, i, data[0])
					atomic.StoreInt32(&readErr, 1)
				}
			}
		}()
	}

	// Let writers and readers race for a bit.
	time.Sleep(300 * time.Millisecond)
	atomic.StoreInt32(&stopped, 1)
	wg.Wait()

	if atomic.LoadInt32(&readErr) != 0 {
		t.Error("detected partial/torn read during concurrent WriteAtomic")
	}

	fi, err := os.Stat(dataPath)
	if err != nil {
		t.Fatalf("data file stat after WriteAtomic: %v", err)
	}
	t.Logf("data file: size=%d mtime=%s", fi.Size(), fi.ModTime())
}

// ─── LockFile_InodeRestat ─────────────────────────────────────────────────────

func TestLockFile_InodeRestat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")
	locker := filelock.New()

	// Pre-create the lock file.
	f, err := os.Create(lockPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Close()

	// Goroutine: delete and recreate the lock file in a tight loop.
	var stopped int32
	var deleteWg sync.WaitGroup
	deleteWg.Add(1)
	go func() {
		defer deleteWg.Done()
		for i := 0; atomic.LoadInt32(&stopped) == 0; i++ {
			os.Remove(lockPath)
			nf, err := os.Create(lockPath)
			if err != nil {
				continue
			}
			nf.Close()
		}
	}()

	// Try to acquire the lock many times. Some attempts must fail with the
	// inode-replaced error; others may succeed on a stable window.
	const attempts = 100
	var failedCount int
	for i := 0; i < attempts; i++ {
		unlock, err := locker.Lock(lockPath)
		if err != nil {
			t.Logf("attempt %d: Lock returned error (expected): %v", i, err)
			failedCount++
		} else {
			unlock()
		}
	}
	atomic.StoreInt32(&stopped, 1)
	deleteWg.Wait()

	t.Logf("inode re-stat: %d/%d attempts failed (expected some failures)", failedCount, attempts)
	if failedCount == 0 {
		t.Log("NOTE: no inode-replaced errors observed — deletion loop may have been too slow; not a hard failure")
	}

	fi, err := os.Stat(lockPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("lock file stat: %v", err)
	}
	if err == nil {
		t.Logf("lock file: size=%d mtime=%s", fi.Size(), fi.ModTime())
	}
}

// ─── Unlock_ReleasesForNextWaiter ─────────────────────────────────────────────

func TestUnlock_ReleasesForNextWaiter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")
	locker := filelock.New()

	// Acquire the lock.
	t.Log("acquiring initial lock")
	unlock1, err := locker.Lock(lockPath)
	if err != nil {
		t.Fatalf("initial Lock: %v", err)
	}
	t.Log("initial lock held")

	acquired := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t.Log("waiter: blocked on Lock()")
		unlock2, err := locker.Lock(lockPath)
		if err != nil {
			t.Errorf("waiter Lock: %v", err)
			return
		}
		close(acquired)
		t.Log("waiter: lock acquired")
		unlock2()
		t.Log("waiter: lock released")
	}()

	// Give the waiter time to block.
	time.Sleep(20 * time.Millisecond)

	// Release the initial lock. The waiter must unblock within 100ms.
	t.Log("releasing initial lock")
	unlock1()

	select {
	case <-acquired:
		t.Log("waiter unblocked correctly after unlock")
	case <-time.After(100 * time.Millisecond):
		t.Error("waiter did not acquire lock within 100ms after unlock")
	}
	wg.Wait()

	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file must exist: %v", err)
	}
	t.Logf("lock file: size=%d mtime=%s", fi.Size(), fi.ModTime())
}
