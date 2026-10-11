// Package github reads edelweiss extension releases from the GitHub REST API (or a compatible server such as the
// dev mock): sources, releases, asset downloads and the asset naming convention.
package github

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidSource is returned for install sources that do not name a GitHub repository.
var ErrInvalidSource = errors.New("not a GitHub repository")

// Source names a repository and optionally a release tag (empty: the latest release).
type Source struct {
	Owner, Repo, Tag string
}

var (
	ownerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	repoRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	tagRe   = regexp.MustCompile(`^[A-Za-z0-9._+/-]{1,128}$`)
)

// ParseSource accepts owner/repo, github.com/owner/repo and https://github.com/owner/repo, each optionally followed
// by @tag, and https://github.com/owner/repo/releases/tag/<tag> (the tag may contain slashes). A trailing "/" or
// ".git" is ignored.
func ParseSource(s string) (Source, error) {
	in := strings.TrimSpace(s)
	rest := in
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "github.com/"} {
		if after, ok := strings.CutPrefix(rest, prefix); ok {
			rest = after
			break
		}
	}
	var src Source
	if path, tag, ok := strings.Cut(rest, "@"); ok {
		if tag == "" {
			return Source{}, fmt.Errorf("%w: %q: empty tag after @", ErrInvalidSource, in)
		}
		rest, src.Tag = path, tag
	}
	parts := strings.Split(strings.TrimSuffix(rest, "/"), "/")
	if len(parts) >= 5 && parts[2] == "releases" && parts[3] == "tag" && src.Tag == "" {
		src.Tag = strings.Join(parts[4:], "/") // tags may contain slashes
		parts = parts[:2]
	}
	if len(parts) != 2 {
		return Source{}, fmt.Errorf("%w: %q (use owner/repo, optionally @tag)", ErrInvalidSource, in)
	}
	src.Owner, src.Repo = parts[0], strings.TrimSuffix(parts[1], ".git")
	if err := src.Validate(); err != nil {
		return Source{}, err
	}
	return src, nil
}

// Validate checks that the source can be put into an API path: a well-formed owner and repository and, if set, a
// tag that is no dot segment and does not start with "-" or start or end with "/". Sources built outside ParseSource
// (e.g. from stored state) must pass it too; Client.Release checks it.
func (s Source) Validate() error {
	if !ownerRe.MatchString(s.Owner) || !repoRe.MatchString(s.Repo) || s.Repo == "." || s.Repo == ".." {
		return fmt.Errorf("%w: %q", ErrInvalidSource, s.Repository())
	}
	if t := s.Tag; t != "" && (!tagRe.MatchString(t) || strings.Contains(t, "..") || t == "." ||
		strings.HasPrefix(t, "/") || strings.HasSuffix(t, "/") || strings.HasPrefix(t, "-")) {
		return fmt.Errorf("%w: %q: invalid tag %q", ErrInvalidSource, s.Repository(), t)
	}
	return nil
}

// Repository is "owner/repo".
func (s Source) Repository() string { return s.Owner + "/" + s.Repo }

// String is the canonical form: owner/repo or owner/repo@tag.
func (s Source) String() string {
	if s.Tag == "" {
		return s.Repository()
	}
	return s.Repository() + "@" + s.Tag
}
