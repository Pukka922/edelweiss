// Package oplock is the lock that keeps the operations that replace parts of the system from running at once:
// installing or updating an extension, and updating edelweiss itself.
package oplock

import "sync"

// Lock is a try-lock; the zero value is unlocked and has no probe. A Lock must not be copied after first use.
type Lock struct {
	mu   sync.Mutex
	busy func() bool
}

// New returns a Lock that also asks busy, with the mutex held, whenever TryLock took the mutex: busy reports an
// operation the mutex does not see, such as the helper of a self-update that outlived the process that started it.
// A nil busy is no probe.
func New(busy func() bool) *Lock { return &Lock{busy: busy} }

// TryLock takes the lock if it is free and the probe reports nothing busy, and reports whether it did. It never
// waits for the mutex; the probe may take a moment.
func (l *Lock) TryLock() bool {
	if !l.mu.TryLock() {
		return false
	}
	if l.busy != nil && l.busy() {
		l.mu.Unlock()
		return false
	}
	return true
}

// Unlock releases the lock taken by TryLock.
func (l *Lock) Unlock() { l.mu.Unlock() }
