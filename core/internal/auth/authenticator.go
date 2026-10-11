package auth

import (
	"fmt"
	"slices"
)

// Authenticator combines password verification, account lookup and policy.
type Authenticator struct {
	Verifier   PasswordVerifier
	Users      UserDB
	DenyUsers  []string
	AdminGroup string
}

// Login verifies the credentials and returns the account.
func (a *Authenticator) Login(name, password string) (*UserInfo, error) {
	if name == "" || password == "" || slices.Contains(a.DenyUsers, name) {
		return nil, ErrInvalidCredentials
	}
	if err := a.Verifier.Verify(name, password); err != nil {
		return nil, err
	}
	u, err := a.Users.Lookup(name)
	if err != nil {
		return nil, fmt.Errorf("lookup user %s: %w", name, err)
	}
	return u, nil
}

// Reauthenticate checks the password of an already logged-in user.
func (a *Authenticator) Reauthenticate(u *UserInfo, password string) error {
	if password == "" {
		return ErrInvalidCredentials
	}
	return a.Verifier.Verify(u.Name, password)
}

// Refresh reads the account of an already logged-in user again, so that
// changes made since the login (group membership above all) take effect.
func (a *Authenticator) Refresh(name string) (*UserInfo, error) {
	u, err := a.Users.Lookup(name)
	if err != nil {
		return nil, fmt.Errorf("lookup user %s: %w", name, err)
	}
	return u, nil
}

// CanAdmin reports whether u may enable admin mode (member of AdminGroup).
func (a *Authenticator) CanAdmin(u *UserInfo) bool {
	return slices.Contains(u.Groups, a.AdminGroup)
}

// Root returns the account used for admin-mode extension processes.
func (a *Authenticator) Root() (*UserInfo, error) {
	return a.Users.Lookup("root")
}
