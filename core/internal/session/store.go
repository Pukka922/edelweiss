// Package session keeps login sessions in memory.
package session

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/Pukka922/edelweiss/core/internal/auth"
)

// Session is a logged-in browser session.
//
// Sessions returned by a Store are shallow copies: changing the returned value
// does not affect the store, but User points at the shared auth.UserInfo and
// must be treated as read-only.
type Session struct {
	ID        string
	CSRFToken string
	User      *auth.UserInfo
	Admin     bool
	Created   time.Time
	LastSeen  time.Time
}

// Store holds sessions with idle and absolute timeouts. It is safe for
// concurrent use. Returned sessions are shallow copies (see Session).
//
// Expired sessions are never returned by Get, SetAdmin or HasSession, but they
// stay in the store until Sweep removes and reports them, so that the caller of
// Sweep can clean up resources tied to them.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
	idle     time.Duration
	maxAge   time.Duration
	now      func() time.Time
}

// NewStore returns an empty store. A session expires when it has not been used
// for longer than idle (see Get) or when it is older than maxAge.
func NewStore(idle, maxAge time.Duration) *Store {
	return &Store{sessions: map[string]*Session{}, idle: idle, maxAge: maxAge, now: time.Now}
}

// SetClock replaces the time source (tests).
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// Create stores a new session for u with a fresh random session ID and CSRF
// token and returns a copy of it. u should be non-nil.
func (s *Store) Create(u *auth.UserInfo) Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	sess := &Session{ID: token(), CSRFToken: token(), User: u, Created: now, LastSeen: now}
	s.sessions[sess.ID] = sess
	return *sess
}

// Get returns a valid session and refreshes its idle timer. An unknown or
// expired session yields false; an expired session is not removed here, only
// Sweep removes expired sessions.
func (s *Store) Get(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	now := s.now()
	if !ok || s.expired(sess, now) {
		return Session{}, false
	}
	sess.LastSeen = now
	return *sess, true
}

// Delete removes the session with the given id (valid or expired) and returns
// it, or false if there is no such session.
func (s *Store) Delete(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return Session{}, false
	}
	delete(s.sessions, id)
	return *sess, true
}

// SetAdmin changes the admin flag and rotates session id and CSRF token. The
// idle timer is refreshed but Created is kept, so rotation does not extend the
// absolute lifetime. An unknown or expired session yields false and is left
// untouched (an expired one is removed only by Sweep).
func (s *Store) SetAdmin(id string, admin bool) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	now := s.now()
	if !ok || s.expired(sess, now) {
		return Session{}, false
	}
	delete(s.sessions, id)
	sess.ID, sess.CSRFToken, sess.Admin, sess.LastSeen = token(), token(), admin, now
	s.sessions[sess.ID] = sess
	return *sess, true
}

// UpdateUser replaces the account of a valid session with a fresh copy of the
// same account (e.g. read again after its groups changed). ID, CSRF token,
// admin flag and timers are kept; copies handed out earlier keep the previous
// account. An unknown or expired session yields false and is left untouched.
func (s *Store) UpdateUser(id string, u *auth.UserInfo) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok || s.expired(sess, s.now()) {
		return Session{}, false
	}
	sess.User = u
	return *sess, true
}

// Sweep removes and returns all expired sessions, in unspecified order. It is
// the only place where expired sessions are removed, so every expired session
// is reported exactly once.
func (s *Store) Sweep() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var out []Session
	for id, sess := range s.sessions {
		if s.expired(sess, now) {
			delete(s.sessions, id)
			out = append(out, *sess)
		}
	}
	return out
}

// HasSession reports whether user has a valid session (an admin one if adminOnly).
// Expired sessions that Sweep has not removed yet and sessions without a user
// are ignored.
func (s *Store) HasSession(user string, adminOnly bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, sess := range s.sessions {
		if sess.User != nil && !s.expired(sess, now) && sess.User.Name == user && (!adminOnly || sess.Admin) {
			return true
		}
	}
	return false
}

func (s *Store) expired(sess *Session, now time.Time) bool {
	return now.Sub(sess.LastSeen) > s.idle || now.Sub(sess.Created) > s.maxAge
}

func token() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
