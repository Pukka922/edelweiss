package oplock

import "testing"

func TestLockTry(t *testing.T) {
	var l Lock
	if !l.TryLock() {
		t.Fatal("a free lock was refused")
	}
	if l.TryLock() {
		t.Fatal("a held lock was granted twice")
	}
	l.Unlock()
	if !l.TryLock() {
		t.Fatal("the lock stayed held after Unlock")
	}
}

// The probe of New is asked with the mutex held, and a busy answer refuses the lock and leaves it free.
func TestLockAsksBusy(t *testing.T) {
	busy, calls := true, 0
	var l *Lock
	l = New(func() bool {
		calls++
		if l.mu.TryLock() {
			l.mu.Unlock()
			t.Error("Busy was asked without the mutex held")
		}
		return busy
	})
	if l.TryLock() {
		t.Fatal("granted while Busy reports busy")
	}
	if !l.mu.TryLock() {
		t.Fatal("the mutex stayed held after a busy answer")
	}
	l.mu.Unlock()
	busy = false
	if !l.TryLock() {
		t.Fatal("refused while Busy reports free")
	}
	if l.TryLock() {
		t.Fatal("a held lock was granted twice")
	}
	if calls != 2 {
		t.Fatalf("Busy asked %d times; want 2 (never while another holds the mutex)", calls)
	}
	l.Unlock()
	if !New(nil).TryLock() {
		t.Fatal("a lock without a probe was refused")
	}
}
