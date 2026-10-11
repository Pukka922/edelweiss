package extmgr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"github.com/Pukka922/edelweiss/core/internal/github"
)

var (
	ErrBusy             = errors.New("another installation is running")
	ErrNoCandidates     = errors.New("the release contains no edelweiss extension for this system")
	ErrNoChecksums      = errors.New("the release has no checksums.txt that lists the extension")
	ErrChecksumMismatch = errors.New("the download does not match its checksum")
	ErrReleaseChanged   = errors.New("the release changed since it was reviewed")
	ErrChooseID         = errors.New("the release contains several extensions; choose one by id")
	ErrUnknownCandidate = errors.New("the release contains no extension with this id")
	ErrNotFromGitHub    = errors.New("the extension was not installed from GitHub")
	ErrUpToDate         = errors.New("the extension is up to date")
)

// Releases is implemented by *github.Client.
type Releases interface {
	Release(ctx context.Context, src github.Source) (*github.Release, error)
	Download(ctx context.Context, a github.Asset, w io.Writer, limit int64) (int64, error)
}

// Candidate is an extension binary of a release that runs on this system.
type Candidate struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
	Asset   string `json:"asset"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Archive bool   `json:"archive"`
	asset   github.Asset
}

// Resolution is a release and the candidates it offers for this system, each with the SHA-256 the release lists.
type Resolution struct {
	Repo        string      `json:"repo"`
	Tag         string      `json:"tag"`
	Name        string      `json:"name"`
	URL         string      `json:"url"`
	PublishedAt time.Time   `json:"publishedAt"`
	Candidates  []Candidate `json:"candidates"`
}

// InstallRequest selects what to install from a release.
type InstallRequest struct {
	Source string // owner/repo[@tag], see github.ParseSource
	ID     string // required if the release offers several extensions
	SHA256 string // optional: the checksum the user confirmed; the install fails if the release now lists another
}

// Update is an installed extension with a newer latest release.
type Update struct {
	ID        string `json:"id"`
	Installed string `json:"installed"`
	Available string `json:"available"`
	Repo      string `json:"repo"`
	Tag       string `json:"tag"`
	Asset     string `json:"asset"`
	SHA256    string `json:"sha256"`
}

// UpdateError is an extension whose update check failed.
type UpdateError struct {
	ID  string
	Err error
}

type InstallerOptions struct {
	Manager     *Manager // may be nil for Resolve only
	Releases    Releases
	Arch        string // the GOARCH extensions must be built for
	MaxDownload int64
	Logger      *slog.Logger
}

// Installer installs and updates extensions from GitHub releases into the store of a Manager.
type Installer struct {
	opts InstallerOptions
	busy sync.Mutex // held by Install and Update; a concurrent call gets ErrBusy
}

func NewInstaller(opts InstallerOptions) *Installer {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Installer{opts: opts}
}

// Resolve looks up the release that source names and the extensions it offers for this system.
func (i *Installer) Resolve(ctx context.Context, source string) (*Resolution, error) {
	src, err := github.ParseSource(source)
	if err != nil {
		return nil, err
	}
	return i.resolve(ctx, src)
}

func (i *Installer) resolve(ctx context.Context, src github.Source) (*Resolution, error) {
	rel, err := i.opts.Releases.Release(ctx, src)
	if err != nil {
		return nil, err
	}
	// The tag comes from the answer, not from the user, and it is shown before anything is confirmed and stored with
	// the extension: git accepts tags with control or bidi characters, so it must pass the check of a typed tag.
	tagged := src
	tagged.Tag = rel.TagName
	if tagged.Validate() != nil {
		return nil, fmt.Errorf("%w: the release has an invalid tag %q", github.ErrUnavailable, rel.TagName)
	}
	res := &Resolution{Repo: src.Repository(), Tag: rel.TagName, Name: rel.Name, URL: rel.HTMLURL,
		PublishedAt: rel.PublishedAt, Candidates: []Candidate{}}
	var lists []github.Asset
	var found []Candidate // every extension asset for this architecture, listed in a checksum list or not
	wanted := map[string]bool{}
	for _, a := range rel.Assets {
		if github.IsChecksums(a.Name) {
			lists = append(lists, a)
			continue
		}
		info, ok := github.ParseAssetName(a.Name)
		if !ok || info.Arch != i.opts.Arch {
			continue
		}
		found = append(found, Candidate{ID: info.ID, Version: info.Version, Arch: info.Arch, Asset: a.Name, Size: a.Size,
			Archive: info.Archive, asset: a})
		wanted[a.Name] = true
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("%w (%s %s, linux/%s)", ErrNoCandidates, res.Repo, res.Tag, i.opts.Arch)
	}
	sums, err := i.checksums(ctx, lists, wanted)
	if err != nil {
		return nil, err
	}
	// Only assets a checksum list vouches for are offered; of those, one candidate per extension id.
	best := map[string]Candidate{}
	var unlisted []string
	for _, c := range found {
		if c.SHA256 = sums[c.Asset]; c.SHA256 == "" {
			unlisted = append(unlisted, c.Asset)
			continue
		}
		if cur, ok := best[c.ID]; !ok || preferred(c, cur) {
			best[c.ID] = c
		}
	}
	if len(best) == 0 {
		return nil, fmt.Errorf("%w: %s not listed", ErrNoChecksums, strings.Join(unlisted, ", "))
	}
	res.Candidates = slices.SortedFunc(maps.Values(best), func(a, b Candidate) int { return strings.Compare(a.ID, b.ID) })
	return res, nil
}

// preferred reports whether a is the better of two assets of the same extension: the higher version, then the raw
// binary (it needs no unpacking), then the greater asset name, so that the result does not depend on the order in
// which the API lists the assets.
func preferred(a, b Candidate) bool {
	if c := semver.Compare("v"+a.Version, "v"+b.Version); c != 0 {
		return c > 0
	}
	if a.Archive != b.Archive {
		return !a.Archive
	}
	return a.Asset > b.Asset
}

// maxChecksumLists bounds how many checksum lists Resolve downloads before the user has confirmed anything.
const maxChecksumLists = 8

// checksums downloads and merges the release's checksum lists, keeping the entries of the wanted file names.
func (i *Installer) checksums(ctx context.Context, lists []github.Asset, wanted map[string]bool) (map[string]string, error) {
	if len(lists) == 0 {
		return nil, fmt.Errorf("%w (no checksums.txt in the release)", ErrNoChecksums)
	}
	if len(lists) > maxChecksumLists {
		return nil, fmt.Errorf("%w: too many checksum lists (%d, at most %d)", ErrNoChecksums, len(lists), maxChecksumLists)
	}
	all := map[string]string{}
	for _, a := range lists {
		var buf bytes.Buffer
		if _, err := i.opts.Releases.Download(ctx, a, &buf, github.MaxChecksumsSize); err != nil {
			return nil, err
		}
		sums, err := github.ParseChecksums(&buf)
		if err != nil {
			return nil, fmt.Errorf("%w: %q: %v", ErrNoChecksums, a.Name, err)
		}
		for name, sum := range sums {
			if !wanted[name] {
				continue
			}
			if prev, ok := all[name]; ok && prev != sum {
				return nil, fmt.Errorf("%w: the checksum lists disagree about %s", ErrNoChecksums, name)
			}
			all[name] = sum
		}
	}
	return all, nil
}

// Pick chooses the candidate with id, or the only one when id is empty.
func Pick(res *Resolution, id string) (Candidate, error) {
	if id == "" {
		if len(res.Candidates) == 1 {
			return res.Candidates[0], nil
		}
		ids := make([]string, len(res.Candidates))
		for k, c := range res.Candidates {
			ids[k] = c.ID
		}
		return Candidate{}, fmt.Errorf("%w (%s)", ErrChooseID, strings.Join(ids, ", "))
	}
	for _, c := range res.Candidates {
		if c.ID == id {
			return c, nil
		}
	}
	return Candidate{}, fmt.Errorf("%w %q (%s %s)", ErrUnknownCandidate, id, res.Repo, res.Tag)
}

// Install downloads, verifies and installs an extension from a GitHub release.
func (i *Installer) Install(ctx context.Context, req InstallRequest) (*Extension, error) {
	if !i.busy.TryLock() {
		return nil, ErrBusy
	}
	defer i.busy.Unlock()
	res, err := i.Resolve(ctx, req.Source)
	if err != nil {
		return nil, err
	}
	c, err := Pick(res, req.ID)
	if err != nil {
		return nil, err
	}
	if err := i.checkConfirmed(res, c, req.SHA256); err != nil {
		return nil, err
	}
	return i.installCandidate(ctx, res, c)
}

// checkConfirmed fails with ErrReleaseChanged if the user confirmed a checksum (not empty) other than the one the
// release lists now.
func (i *Installer) checkConfirmed(res *Resolution, c Candidate, confirmed string) error {
	if confirmed == "" || strings.EqualFold(confirmed, c.SHA256) {
		return nil
	}
	i.opts.Logger.Warn("release changed since it was reviewed", "repo", res.Repo, "tag", res.Tag, "asset", c.Asset,
		"confirmed", confirmed, "listed", c.SHA256)
	return fmt.Errorf("%w: %s now has SHA-256 %s", ErrReleaseChanged, c.Asset, c.SHA256)
}

func (i *Installer) installCandidate(ctx context.Context, res *Resolution, c Candidate) (*Extension, error) {
	// "install-" prefix: Manager.New sweeps what an interrupted download leaves behind
	dir, err := os.MkdirTemp(i.opts.Manager.opts.TmpDir, "install-dl-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "asset")
	sum, err := i.download(ctx, c, file)
	if err != nil {
		return nil, err
	}
	if sum != c.SHA256 {
		i.opts.Logger.Warn("download does not match its checksum", "repo", res.Repo, "tag", res.Tag, "asset", c.Asset,
			"listed", c.SHA256, "downloaded", sum)
		return nil, fmt.Errorf("%w: %s has SHA-256 %s, the release lists %s", ErrChecksumMismatch, c.Asset, sum, c.SHA256)
	}
	bin := file
	if c.Archive {
		bin = filepath.Join(dir, "backend")
		if err := extractExecutable(file, bin, i.opts.MaxDownload); err != nil {
			return nil, err
		}
	}
	src := Source{Type: "github", Location: "github.com/" + res.Repo, Repo: res.Repo, Tag: res.Tag, Asset: c.Asset}
	return i.opts.Manager.Install(ctx, bin, src, Expect{ID: c.ID, Version: c.Version})
}

func (i *Installer) download(ctx context.Context, c Candidate, path string) (string, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = i.opts.Releases.Download(ctx, c.asset, io.MultiWriter(f, h), i.opts.MaxDownload)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Updates checks every extension installed from GitHub against the latest release of its repository.
func (i *Installer) Updates(ctx context.Context) ([]Update, []UpdateError) {
	updates := []Update{}
	var failed []UpdateError
	for _, e := range i.opts.Manager.List() {
		if e.Dev || e.Source.Type != "github" {
			continue
		}
		res, c, err := i.latest(ctx, e)
		switch {
		case errors.Is(err, ErrUpToDate):
		case err != nil:
			failed = append(failed, UpdateError{ID: e.Manifest.ID, Err: err})
		default:
			updates = append(updates, Update{ID: c.ID, Installed: e.Manifest.Version, Available: c.Version,
				Repo: res.Repo, Tag: res.Tag, Asset: c.Asset, SHA256: c.SHA256})
		}
	}
	return updates, failed
}

// Update installs the latest release of an extension installed from GitHub if it is newer than the installed one.
// confirmed is the checksum the user confirmed (Update.SHA256 of Updates); if it is not empty and the latest release
// lists another one, nothing is installed and the error is ErrReleaseChanged.
func (i *Installer) Update(ctx context.Context, id, confirmed string) (*Extension, error) {
	if !i.busy.TryLock() {
		return nil, ErrBusy
	}
	defer i.busy.Unlock()
	e, ok := i.opts.Manager.Get(id)
	if !ok {
		return nil, ErrNotFound
	}
	if e.Dev || e.Source.Type != "github" {
		return nil, fmt.Errorf("%w: %s", ErrNotFromGitHub, id)
	}
	res, c, err := i.latest(ctx, e)
	if err != nil {
		return nil, err
	}
	if err := i.checkConfirmed(res, c, confirmed); err != nil {
		return nil, err
	}
	return i.installCandidate(ctx, res, c)
}

// latest resolves the latest release of e's repository and its candidate for e; ErrUpToDate if it is not newer.
func (i *Installer) latest(ctx context.Context, e *Extension) (*Resolution, Candidate, error) {
	src, err := github.ParseSource(e.Source.Repo)
	if err != nil {
		return nil, Candidate{}, err
	}
	res, err := i.resolve(ctx, src)
	if err != nil {
		return nil, Candidate{}, err
	}
	c, err := Pick(res, e.Manifest.ID)
	if err != nil {
		return nil, Candidate{}, err
	}
	if semver.Compare("v"+c.Version, "v"+e.Manifest.Version) <= 0 {
		return res, c, fmt.Errorf("%w (%s %s)", ErrUpToDate, e.Manifest.ID, e.Manifest.Version)
	}
	return res, c, nil
}
