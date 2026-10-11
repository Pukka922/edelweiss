package auth

import (
	"fmt"
	"os/user"
	"strconv"
)

// UserInfo describes a local account.
type UserInfo struct {
	Name     string   `json:"name"`
	UID      int      `json:"uid"`
	GID      int      `json:"gid"`
	Home     string   `json:"home"`
	Groups   []string `json:"groups"`
	GroupIDs []int    `json:"-"`
}

// UserDB looks up accounts.
type UserDB interface {
	Lookup(name string) (*UserInfo, error)
}

// OSUserDB reads /etc/passwd and /etc/group (pure Go with CGO_ENABLED=0).
type OSUserDB struct{}

// Lookup returns the account and all its groups (primary and supplementary).
func (OSUserDB) Lookup(name string) (*UserInfo, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return nil, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, fmt.Errorf("uid of %s: %w", name, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, fmt.Errorf("gid of %s: %w", name, err)
	}
	info := &UserInfo{Name: u.Username, UID: uid, GID: gid, Home: u.HomeDir}
	ids, err := u.GroupIds()
	if err != nil {
		return nil, fmt.Errorf("groups of %s: %w", name, err)
	}
	for _, id := range ids {
		n, err := strconv.Atoi(id)
		if err != nil {
			continue
		}
		info.GroupIDs = append(info.GroupIDs, n)
		if g, err := user.LookupGroupId(id); err == nil {
			info.Groups = append(info.Groups, g.Name)
		}
	}
	return info, nil
}
