package filelock

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLockExcludesAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "x.lock")
	unlock, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path, 50*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("second lock while held: %v, want ErrTimeout", err)
	}
	unlock()
	unlock2, err := Lock(path, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock2()
}

func TestLockSerializesCriticalSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	var (
		mu      sync.Mutex
		inside  int
		overlap bool
		wg      sync.WaitGroup
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				unlock, err := Lock(path, 5*time.Second)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				inside++
				if inside > 1 {
					overlap = true
				}
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				unlock()
			}
		}()
	}
	wg.Wait()
	if overlap {
		t.Fatal("two holders inside the critical section")
	}
}
