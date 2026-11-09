package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Pukka922/edelweiss/core/internal/github"
	"golang.org/x/mod/semver"
)

// Releases is the part of the GitHub client the update needs.
type Releases interface {
	Release(ctx context.Context, src github.Source) (*github.Release, error)
	Download(ctx context.Context, a github.Asset, w io.Writer, limit int64) (int64, error)
}

// Offer is what GitHub has to say about a newer edelweissd for this machine.
type Offer struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest,omitempty"`
	Available bool      `json:"available"`
	Release   string    `json:"release,omitempty"` // tag, e.g. "v0.3.1"
	Published time.Time `json:"published,omitzero"`
	NotesURL  string    `json:"notesUrl,omitempty"` // html_url
	Asset     string    `json:"asset,omitempty"`
	Size      int64     `json:"size,omitempty"`
	SHA256    string    `json:"sha256,omitempty"`
	asset     github.Asset
}

// Source is where edelweiss releases are published.
var Source = github.Source{Owner: "Pukka922", Repo: "edelweiss"}

var (
	ErrNoAsset           = errors.New("the release has no edelweissd binary for this architecture")
	ErrNoChecksums       = errors.New("the release has no checksum for the edelweissd binary")
	ErrChecksumsDisagree = errors.New("the checksum lists of the release disagree")
)

// DevVersion is the version of a build that did not come from a release.
const DevVersion = "0.0.0-dev"

// maxChecksumLists bounds how many checksum lists Check downloads.
const maxChecksumLists = 8

// Check asks GitHub for the latest release and offers it when it is a newer, checksummed edelweissd for arch. A
// development build is never offered anything, and GitHub is not asked.
func Check(ctx context.Context, r Releases, src github.Source, arch, current string) (Offer, error) {
	off := Offer{Current: current}
	if current == DevVersion || !semver.IsValid("v"+current) {
		return off, nil
	}
	src.Tag = ""
	rel, err := r.Release(ctx, src)
	if err != nil {
		return off, err
	}
	if rel.Draft || rel.Prerelease {
		return off, nil
	}
	version := strings.TrimPrefix(rel.TagName, "v")
	if !semver.IsValid("v" + version) {
		return off, fmt.Errorf("%w: the release tag %q is not a version", ErrNoAsset, rel.TagName)
	}
	if semver.Prerelease("v"+version) != "" {
		return off, nil
	}
	var bin github.Asset
	var lists []github.Asset
	found := false
	for _, a := range rel.Assets {
		if github.IsChecksums(a.Name) {
			lists = append(lists, a)
			continue
		}
		if v, ar, ok := github.ParseCoreAsset(a.Name); ok && ar == arch && v == version && !found {
			bin, found = a, true
		}
	}
	if !found {
		return off, fmt.Errorf("%w (%s, linux/%s)", ErrNoAsset, rel.TagName, arch)
	}
	if bin.Size <= 0 {
		return off, fmt.Errorf("%w: %s has no size", ErrNoAsset, bin.Name)
	}
	off.Latest = version
	off.Release = rel.TagName
	off.Published = rel.PublishedAt
	off.NotesURL = rel.HTMLURL
	if semver.Compare("v"+version, "v"+current) <= 0 {
		return off, nil
	}
	sum, err := checksum(ctx, r, lists, bin.Name)
	if err != nil {
		return off, err
	}
	off.Available = true
	off.Asset, off.Size, off.SHA256, off.asset = bin.Name, bin.Size, sum, bin
	return off, nil
}

// checksum reads the checksum lists of the release and returns the digest they give for name.
func checksum(ctx context.Context, r Releases, lists []github.Asset, name string) (string, error) {
	if len(lists) == 0 {
		return "", fmt.Errorf("%w (no checksums.txt in the release)", ErrNoChecksums)
	}
	if len(lists) > maxChecksumLists {
		return "", fmt.Errorf("%w: too many checksum lists (%d, at most %d)", ErrNoChecksums, len(lists), maxChecksumLists)
	}
	var sum string
	for _, a := range lists {
		var buf bytes.Buffer
		if _, err := r.Download(ctx, a, &buf, github.MaxChecksumsSize); err != nil {
			return "", err
		}
		sums, err := github.ParseChecksums(&buf)
		if err != nil {
			return "", fmt.Errorf("%w: %q: %v", ErrNoChecksums, a.Name, err)
		}
		s, ok := sums[name]
		if !ok {
			continue
		}
		if sum != "" && sum != s {
			return "", fmt.Errorf("%w about %s", ErrChecksumsDisagree, name)
		}
		sum = s
	}
	if sum == "" {
		return "", fmt.Errorf("%w: %s is not listed", ErrNoChecksums, name)
	}
	return sum, nil
}
