package session

import (
	"testing"
	"time"

	"github.com/Pukka922/edelweiss/core/internal/auth"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newStore() (*Store, *clock) {
	c := &clock{t: time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)}
	s := NewStore(15*time.Minute, 12*time.Hour)
	s.SetClock(c.now)
	return s, c
}

var alice = &auth.UserInfo{Name: "alice", UID: 1000}

func TestCreateGet(t *testing.T) {
	s, _ := newStore()
	sess := s.Create(alice)
	if len(sess.ID) < 40 || len(sess.CSRFToken) < 40 || sess.ID == sess.CSRFToken {
		t.Fatalf("weak tokens: %+v", sess)
	}
	got, ok := s.Get(sess.ID)
	if !ok || got.User.Name != "alice" || got.Admin {
		t.Fatalf("Get = %+v, %v", got, ok)
	}
	if _, ok := s.Get("nope"); ok {
		t.Fatal("unknown id found")
	}
}

func TestIdleExpiryAndTouch(t *testing.T) {
	s, c := newStore()
	sess := s.Create(alice)
	c.advance(10 * time.Minute)
	if _, ok := s.Get(sess.ID); !ok {
		t.Fatal("expired too early")
	}
	c.advance(10 * time.Minute) // 10 min since last touch
	if _, ok := s.Get(sess.ID); !ok {
		t.Fatal("touch did not extend session")
	}
	c.advance(16 * time.Minute)
	if _, ok := s.Get(sess.ID); ok {
		t.Fatal("idle session still valid")
	}
}

func TestMaxAge(t *testing.T) {
	s, c := newStore() // idle 15m, max age 12h
	sess := s.Create(alice)
	for i := 1; i <= 47; i++ { // keep active with 15 minute touches up to 11h45m
		c.advance(15 * time.Minute)
		if _, ok := s.Get(sess.ID); !ok {
			t.Fatalf("session expired too early after %v", time.Duration(i)*15*time.Minute)
		}
	}
	c.advance(14 * time.Minute) // 11h59m, still active
	if _, ok := s.Get(sess.ID); !ok {
		t.Fatal("session expired before max age")
	}
	// 12h00m01s since creation but only 61s idle: only the absolute limit can expire it.
	c.advance(61 * time.Second)
	if _, ok := s.Get(sess.ID); ok {
		t.Fatal("session survived max age")
	}
}

func TestSetAdminRotatesID(t *testing.T) {
	s, _ := newStore()
	sess := s.Create(alice)
	adm, ok := s.SetAdmin(sess.ID, true)
	if !ok || !adm.Admin || adm.ID == sess.ID || adm.CSRFToken == sess.CSRFToken {
		t.Fatalf("SetAdmin = %+v", adm)
	}
	if _, ok := s.Get(sess.ID); ok {
		t.Fatal("old id still valid")
	}
	if !s.HasSession("alice", true) || s.HasSession("bob", false) {
		t.Fatal("HasSession wrong")
	}
	back, _ := s.SetAdmin(adm.ID, false)
	if back.Admin || s.HasSession("alice", true) || !s.HasSession("alice", false) {
		t.Fatal("admin flag not cleared")
	}
}

func TestDeleteAndSweep(t *testing.T) {
	s, c := newStore()
	a := s.Create(alice)
	b := s.Create(&auth.UserInfo{Name: "bob"})
	if _, ok := s.Delete(a.ID); !ok {
		t.Fatal("delete failed")
	}
	c.advance(20 * time.Minute)
	expired := s.Sweep()
	if len(expired) != 1 || expired[0].ID != b.ID {
		t.Fatalf("Sweep = %+v", expired)
	}
	if s.HasSession("bob", false) {
		t.Fatal("swept session still present")
	}
}

func TestExpiredGetKeepsSessionForSweep(t *testing.T) {
	s, c := newStore()
	sess := s.Create(alice)
	c.advance(16 * time.Minute) // past the idle timeout
	if _, ok := s.Get(sess.ID); ok {
		t.Fatal("expired session returned by Get")
	}
	// Get must not have removed it: Sweep is the only place that reports expired
	// sessions, and its callers use the result to clean up per-user resources.
	expired := s.Sweep()
	if len(expired) != 1 || expired[0].ID != sess.ID || expired[0].User.Name != "alice" {
		t.Fatalf("Sweep = %+v, want exactly the expired session %q", expired, sess.ID)
	}
	if again := s.Sweep(); len(again) != 0 {
		t.Fatalf("second Sweep = %+v, want none", again)
	}
}

func TestExpiredSetAdminKeepsSessionForSweep(t *testing.T) {
	s, c := newStore()
	sess := s.Create(alice)
	c.advance(16 * time.Minute) // past the idle timeout
	if got, ok := s.SetAdmin(sess.ID, true); ok || got.ID != "" {
		t.Fatalf("SetAdmin on expired session = %+v, %v", got, ok)
	}
	expired := s.Sweep()
	if len(expired) != 1 || expired[0].ID != sess.ID || expired[0].Admin {
		t.Fatalf("Sweep = %+v, want exactly the unmodified expired session %q", expired, sess.ID)
	}
	if again := s.Sweep(); len(again) != 0 {
		t.Fatalf("second Sweep = %+v, want none", again)
	}
}

func TestSetAdminPreservesCreated(t *testing.T) {
	s, c := newStore()
	sess := s.Create(alice)
	c.advance(10 * time.Minute)
	adm, ok := s.SetAdmin(sess.ID, true)
	if !ok {
		t.Fatal("SetAdmin failed")
	}
	if !adm.Created.Equal(sess.Created) {
		t.Fatalf("Created = %v, want %v (rotation must not extend the absolute lifetime)", adm.Created, sess.Created)
	}
	if !adm.LastSeen.Equal(c.now()) {
		t.Fatalf("LastSeen = %v, want %v", adm.LastSeen, c.now())
	}
	got, ok := s.Get(adm.ID)
	if !ok || !got.Created.Equal(sess.Created) {
		t.Fatalf("Get after rotation = %+v, %v", got, ok)
	}
}

// UpdateUser swaps in a fresh copy of the account (e.g. changed groups) without
// changing anything else about the session.
func TestUpdateUserKeepsTheSession(t *testing.T) {
	s, c := newStore()
	sess := s.Create(alice)
	c.advance(time.Minute)
	fresh := &auth.UserInfo{Name: "alice", UID: 1000, Groups: []string{"alice", "docker"}}
	got, ok := s.UpdateUser(sess.ID, fresh)
	if !ok || got.User != fresh || got.ID != sess.ID || got.CSRFToken != sess.CSRFToken || got.Admin ||
		!got.Created.Equal(sess.Created) {
		t.Fatalf("UpdateUser = %+v, %v", got, ok)
	}
	if stored, ok := s.Get(sess.ID); !ok || stored.User != fresh {
		t.Fatalf("Get after UpdateUser = %+v, %v", stored, ok)
	}
	if sess.User != alice {
		t.Fatal("UpdateUser changed a copy handed out earlier")
	}
	if got, ok := s.UpdateUser("nope", fresh); ok || got.User != nil {
		t.Fatalf("UpdateUser(unknown) = %+v, %v", got, ok)
	}
	c.advance(16 * time.Minute) // past the idle timeout
	if _, ok := s.UpdateUser(sess.ID, alice); ok {
		t.Fatal("UpdateUser revived an expired session")
	}
	if expired := s.Sweep(); len(expired) != 1 || expired[0].User != fresh {
		t.Fatalf("Sweep = %+v, want the expired session unmodified", expired)
	}
}

func TestSetAdminUnknownID(t *testing.T) {
	s, _ := newStore()
	s.Create(alice)
	if got, ok := s.SetAdmin("nope", true); ok || got.ID != "" || got.User != nil {
		t.Fatalf("SetAdmin(unknown) = %+v, %v", got, ok)
	}
	if s.HasSession("alice", true) {
		t.Fatal("unknown id changed another session")
	}
}

func TestHasSessionIgnoresExpiredUnswept(t *testing.T) {
	s, c := newStore()
	sess := s.Create(alice)
	if !s.HasSession("alice", false) {
		t.Fatal("fresh session not found")
	}
	c.advance(16 * time.Minute) // expired but not yet swept
	if s.HasSession("alice", false) {
		t.Fatal("HasSession counted an expired session")
	}
	if expired := s.Sweep(); len(expired) != 1 || expired[0].ID != sess.ID {
		t.Fatalf("session was not still pending for Sweep: %+v", expired)
	}
}

func TestHasSessionSkipsNilUser(t *testing.T) {
	s, _ := newStore()
	s.Create(nil)
	s.Create(alice)
	if s.HasSession("bob", false) {
		t.Fatal("HasSession(bob) = true")
	}
	if !s.HasSession("alice", false) {
		t.Fatal("HasSession(alice) = false")
	}
}
