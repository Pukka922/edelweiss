package auth

import (
	"errors"
	"testing"
)

type fakeVerifier map[string]string

func (f fakeVerifier) Verify(name, password string) error {
	if pw, ok := f[name]; ok && pw == password {
		return nil
	}
	return ErrInvalidCredentials
}

type fakeUsers map[string]*UserInfo

func (f fakeUsers) Lookup(name string) (*UserInfo, error) {
	if u, ok := f[name]; ok {
		return u, nil
	}
	return nil, errors.New("unknown user")
}

func newTestAuthenticator() *Authenticator {
	return &Authenticator{
		Verifier: fakeVerifier{"alice": "pw-a", "bob": "pw-b", "root": "pw-r"},
		Users: fakeUsers{
			"alice": {Name: "alice", UID: 1000, Groups: []string{"alice", "wheel"}},
			"bob":   {Name: "bob", UID: 1001, Groups: []string{"bob"}},
			"root":  {Name: "root", UID: 0, Groups: []string{"root", "wheel"}},
		},
		DenyUsers:  []string{"root"},
		AdminGroup: "wheel",
	}
}

func TestLogin(t *testing.T) {
	a := newTestAuthenticator()
	u, err := a.Login("alice", "pw-a")
	if err != nil || u.UID != 1000 {
		t.Fatalf("alice: %v %+v", err, u)
	}
	if _, err := a.Login("alice", "nope"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := a.Login("root", "pw-r"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("denied user: %v", err)
	}
	if _, err := a.Login("", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("empty: %v", err)
	}
}

var errVerifierDown = errors.New("shadow file unreadable")

type failingVerifier struct{}

func (failingVerifier) Verify(string, string) error { return errVerifierDown }

func TestLoginPassesThroughOperationalVerifierError(t *testing.T) {
	a := newTestAuthenticator()
	a.Verifier = failingVerifier{}
	u, err := a.Login("alice", "pw-a")
	if !errors.Is(err, errVerifierDown) {
		t.Fatalf("expected the verifier error to pass through, got %v", err)
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("operational error must not look like a credential failure: %v", err)
	}
	if u != nil {
		t.Fatalf("no user expected on error, got %+v", u)
	}

	alice := &UserInfo{Name: "alice"}
	if err := a.Reauthenticate(alice, "pw-a"); !errors.Is(err, errVerifierDown) || errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Reauthenticate: expected the verifier error to pass through, got %v", err)
	}
}

func TestLoginUserLookupFailureAfterVerifyIsOperationalError(t *testing.T) {
	a := newTestAuthenticator()
	a.Verifier = fakeVerifier{"ghost": "pw-g"} // password ok, but no passwd entry
	u, err := a.Login("ghost", "pw-g")
	if err == nil {
		t.Fatalf("expected error, got user %+v", u)
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("lookup failure must not look like a credential failure: %v", err)
	}
	if u != nil {
		t.Fatalf("no user expected on error, got %+v", u)
	}
}

// Refresh reads the account again, so group changes made after the login count.
func TestRefreshLooksTheAccountUpAgain(t *testing.T) {
	a := newTestAuthenticator()
	alice, err := a.Login("alice", "pw-a")
	if err != nil {
		t.Fatal(err)
	}
	a.Users.(fakeUsers)["alice"] = &UserInfo{Name: "alice", UID: 1000, Groups: []string{"alice"}} // left wheel
	fresh, err := a.Refresh("alice")
	if err != nil || fresh.Name != "alice" || a.CanAdmin(fresh) || !a.CanAdmin(alice) {
		t.Fatalf("Refresh = %+v, %v", fresh, err)
	}
	if u, err := a.Refresh("ghost"); err == nil || u != nil || errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Refresh(unknown) = %+v, %v; want an operational error", u, err)
	}
}

func TestCanAdminAndReauthenticate(t *testing.T) {
	a := newTestAuthenticator()
	alice, _ := a.Login("alice", "pw-a")
	bob, _ := a.Login("bob", "pw-b")
	if !a.CanAdmin(alice) || a.CanAdmin(bob) {
		t.Fatal("CanAdmin wrong")
	}
	if err := a.Reauthenticate(alice, "pw-a"); err != nil {
		t.Fatal(err)
	}
	if err := a.Reauthenticate(alice, "x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("reauth with wrong password succeeded")
	}
	root, err := a.Root()
	if err != nil || root.UID != 0 {
		t.Fatalf("Root() = %+v, %v", root, err)
	}
}
