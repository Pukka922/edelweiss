package extmgr

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss/core/internal/apkdb"
	"github.com/Pukka922/edelweiss/core/internal/config"
	"github.com/Pukka922/edelweiss/sdk/manifest"
)

// ErrNotFound is returned for unknown extension ids.
var ErrNotFound = errors.New("extension not found")

// ErrInvalidExtension wraps the reasons a binary is refused as an extension: the manifest query failed (not an
// executable for this system, dynamically linked, invalid manifest, unsupported apiVersion) or the manifest does not
// say what was expected.
var ErrInvalidExtension = errors.New("not a valid edelweiss extension")

// ErrDataNotRemoved is what the error of Remove matches (errors.Is) when the extension itself was removed but its
// data directory could not be deleted; errors.As gives the *DataNotRemovedError with the directory and the cause.
var ErrDataNotRemoved = errors.New("data directory of the extension not removed")

// DataNotRemovedError is the error of a Remove that removed the extension but could not delete its data directory.
// Nothing is left to retry with: the extension is gone, only the directory has to be deleted by hand.
type DataNotRemovedError struct {
	Dir string // the data directory that is still there
	Err error  // why it could not be deleted
}

func (e *DataNotRemovedError) Error() string {
	return fmt.Sprintf("%v: %s: %v", ErrDataNotRemoved, e.Dir, e.Err)
}

// Unwrap lets errors.Is match both ErrDataNotRemoved and the cause (fs.ErrPermission, for example).
func (e *DataNotRemovedError) Unwrap() []error { return []error{ErrDataNotRemoved, e.Err} }

// Expect is what an installed manifest must say; empty fields are not checked.
type Expect struct {
	ID      string
	Version string
}

const binaryName = "backend"

// DefaultExtDataRoot is where extensions keep their data by convention: <root>/edelweiss-ext-<id>.
const DefaultExtDataRoot = "/var/lib"

// leftoverMaxAge is how old (by modification time) the leftovers of an
// interrupted install or removal must be before New sweeps them; younger ones
// may belong to an operation another process is running right now.
const leftoverMaxAge = time.Hour

// Source records where an installed extension came from.
type Source struct {
	Type        string    `json:"type"` // "file", "github" or "dev"
	Location    string    `json:"location"`
	Repo        string    `json:"repo,omitempty"`
	Tag         string    `json:"tag,omitempty"`
	Asset       string    `json:"asset,omitempty"`
	SHA256      string    `json:"sha256"`
	InstalledAt time.Time `json:"installedAt"`
}

// Extension is an installed (or dev) extension.
type Extension struct {
	Manifest *manifest.Manifest
	Dir      string // store directory, empty for dev extensions
	Binary   string
	Source   Source
	UIDir    string // dev only: serve UI from this directory
	Dev      bool
}

type Options struct {
	Dir     string // <state>/extensions
	TmpDir  string // <state>/tmp, same filesystem as Dir
	Querier Querier
	// Packages checks the Alpine packages an extension requires; nil means apk's database at apkdb.DefaultPath.
	Packages PackageChecker
	// ExtDataRoot is the directory below which extensions keep their data (DefaultExtDataRoot in production); Remove
	// deletes <ExtDataRoot>/edelweiss-ext-<id>. Empty or not an absolute path: data directories are left alone.
	ExtDataRoot string
	Dev         []config.DevExtension
	Logger      *slog.Logger
	// OnChange is called when an extension was added, replaced or removed. It
	// runs while the manager's operation lock is held, so it must not call
	// Load, InstallFile or Remove synchronously.
	OnChange func(id string)
	Now      func() time.Time
}

// Manager owns the extension store.
type Manager struct {
	opts Options
	mu   sync.RWMutex
	exts map[string]*Extension
	opMu sync.Mutex // serializes install/remove/load
}

func New(opts Options) (*Manager, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.OnChange == nil {
		opts.OnChange = func(string) {}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Packages == nil {
		opts.Packages = apkdb.New(apkdb.DefaultPath)
	}
	if err := mkdirMode(opts.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := mkdirMode(opts.TmpDir, 0o711); err != nil {
		return nil, err
	}
	m := &Manager{opts: opts, exts: map[string]*Extension{}}
	m.sweep(opts.Dir, ".old-")
	m.sweep(opts.TmpDir, "install-")
	return m, nil
}

// sweep removes the entries of dir whose name starts with prefix and whose
// modification time is older than leftoverMaxAge: leftovers of installs or
// removals that were interrupted (crash, kill). Younger entries are kept, they
// may belong to an operation that another process is running right now.
func (m *Manager) sweep(dir, prefix string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		m.opts.Logger.Warn("cannot scan for leftovers", "dir", dir, "err", err)
		return
	}
	now := m.opts.Now()
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < leftoverMaxAge {
			continue
		}
		path := filepath.Join(dir, e.Name())
		m.opts.Logger.Info("removing leftover of an interrupted operation", "path", path)
		m.removeAll(path)
	}
}

// removeAll deletes path; a failure only leaves garbage behind, so it is
// logged instead of returned.
func (m *Manager) removeAll(path string) {
	if err := os.RemoveAll(path); err != nil {
		m.opts.Logger.Warn("cannot remove directory", "path", path, "err", err)
	}
}

func mkdirMode(dir string, mode os.FileMode) error {
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	return os.Chmod(dir, mode)
}

// Load (re)reads the store and the dev extensions.
func (m *Manager) Load(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	entries, err := os.ReadDir(m.opts.Dir)
	if err != nil {
		return err
	}
	found := map[string]*Extension{}
	for _, e := range entries {
		if !e.IsDir() || !manifest.ValidID(e.Name()) {
			continue
		}
		ext, err := readStored(filepath.Join(m.opts.Dir, e.Name()))
		if err != nil {
			m.opts.Logger.Warn("skipping invalid extension", "dir", e.Name(), "err", err)
			continue
		}
		found[ext.Manifest.ID] = ext
	}
	for _, d := range m.opts.Dev {
		ext, err := m.loadDev(ctx, d)
		if err != nil {
			m.opts.Logger.Warn("skipping dev extension", "binary", d.Binary, "err", err)
			continue
		}
		found[ext.Manifest.ID] = ext
	}

	m.mu.Lock()
	old := m.exts
	m.exts = found
	m.mu.Unlock()

	ids := map[string]bool{}
	for id := range old {
		ids[id] = true
	}
	for id := range found {
		ids[id] = true
	}
	// Changes made by another process (`edelweissd ext …`, whose own log goes
	// nowhere) or a dev rebuild reach the daemon's log here; the daemon's own
	// installs and removals already changed m.exts and logged themselves.
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		switch a, b := old[id], found[id]; {
		case a == nil:
			m.opts.Logger.Info("extension store: added", "id", id, "version", b.Manifest.Version, "source", b.Source.Location)
		case b == nil:
			m.opts.Logger.Info("extension store: removed", "id", id)
		case a.Source.SHA256 != b.Source.SHA256:
			m.opts.Logger.Info("extension store: updated", "id", id, "version", b.Manifest.Version, "source", b.Source.Location)
		default:
			continue
		}
		m.opts.OnChange(id)
	}
	return nil
}

func readStored(dir string) (*Extension, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	man, err := manifest.Parse(data)
	if err != nil {
		return nil, err
	}
	if man.ID != filepath.Base(dir) {
		return nil, fmt.Errorf("manifest id %q does not match directory", man.ID)
	}
	var src Source
	data, err = os.ReadFile(filepath.Join(dir, "source.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &src); err != nil {
		return nil, fmt.Errorf("source.json: %w", err)
	}
	bin := filepath.Join(dir, binaryName)
	if fi, err := os.Stat(bin); err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("missing backend binary")
	}
	return &Extension{Manifest: man, Dir: dir, Binary: bin, Source: src}, nil
}

func (m *Manager) loadDev(ctx context.Context, d config.DevExtension) (*Extension, error) {
	man, err := m.opts.Querier.Query(ctx, d.Binary)
	if err != nil {
		return nil, err
	}
	sum, err := fileSHA256(d.Binary)
	if err != nil {
		return nil, err
	}
	return &Extension{Manifest: man, Binary: d.Binary, UIDir: d.UIDir, Dev: true,
		Source: Source{Type: "dev", Location: d.Binary, SHA256: sum}}, nil
}

// List returns all extensions sorted by menu section, order and id.
func (m *Manager) List() []*Extension {
	m.mu.RLock()
	out := make([]*Extension, 0, len(m.exts))
	for _, e := range m.exts {
		out = append(out, e)
	}
	m.mu.RUnlock()
	slices.SortFunc(out, func(a, b *Extension) int {
		sa := slices.Index(manifest.Sections, a.Manifest.Menu.Section)
		sb := slices.Index(manifest.Sections, b.Manifest.Menu.Section)
		if c := cmp.Compare(sa, sb); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Manifest.Menu.Order, b.Manifest.Menu.Order); c != 0 {
			return c
		}
		return cmp.Compare(a.Manifest.ID, b.Manifest.ID)
	})
	return out
}

func (m *Manager) Get(id string) (*Extension, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.exts[id]
	return e, ok
}

// InstallFile installs the extension binary at path. If expectID is set, the
// manifest id must match it.
func (m *Manager) InstallFile(ctx context.Context, path string, src Source, expectID string) (*Extension, error) {
	return m.Install(ctx, path, src, Expect{ID: expectID})
}

// Install installs the extension binary at path after checking its manifest against want.
func (m *Manager) Install(ctx context.Context, path string, src Source, want Expect) (*Extension, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	staging, err := os.MkdirTemp(m.opts.TmpDir, "install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0o755); err != nil {
		return nil, err
	}
	bin := filepath.Join(staging, binaryName)
	sum, err := copyExecutable(path, bin)
	if err != nil {
		return nil, err
	}
	man, err := m.opts.Querier.Query(ctx, bin)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr // canceled or timed out by the caller: not the binary's fault
		}
		return nil, fmt.Errorf("%w: %w", ErrInvalidExtension, err)
	}
	if want.ID != "" && man.ID != want.ID {
		return nil, fmt.Errorf("%w: extension id %q does not match expected %q", ErrInvalidExtension, man.ID, want.ID)
	}
	if want.Version != "" && man.Version != want.Version {
		return nil, fmt.Errorf("%w: extension version %q does not match expected %q", ErrInvalidExtension, man.Version, want.Version)
	}
	if err := m.checkPackages(man); err != nil {
		return nil, err
	}
	src.SHA256 = sum
	if src.InstalledAt.IsZero() {
		src.InstalledAt = m.opts.Now().UTC()
	}
	if err := writeJSON(filepath.Join(staging, "manifest.json"), man); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(staging, "source.json"), src); err != nil {
		return nil, err
	}
	target := filepath.Join(m.opts.Dir, man.ID)
	if err := m.replaceDir(staging, target); err != nil {
		return nil, err
	}
	ext := &Extension{Manifest: man, Dir: target, Binary: filepath.Join(target, binaryName), Source: src}
	m.mu.Lock()
	m.exts[man.ID] = ext
	m.mu.Unlock()
	m.opts.Logger.Info("extension store: installed", "id", man.ID, "version", man.Version, "source", src.Location)
	m.opts.OnChange(man.ID)
	return ext, nil
}

// ExtDataDir is the directory the extension id keeps its data in by convention, or "" if this manager does not handle
// extension data, its root is not an absolute path (Remove would delete relative to the working directory) or id is
// invalid.
func (m *Manager) ExtDataDir(id string) string {
	if !filepath.IsAbs(m.opts.ExtDataRoot) || !manifest.ValidID(id) {
		return ""
	}
	return filepath.Join(m.opts.ExtDataRoot, "edelweiss-ext-"+id)
}

// Remove deletes an installed extension and then its data directory (ExtDataDir), if there is one; a symlink is
// removed as a link. OnChange has been called by then: when the running daemon removes the extension it has stopped
// the extension's processes before the directory goes. With `edelweissd ext remove` OnChange does nothing, the
// processes (if the daemon runs them) are stopped right after, when the daemon notices the change, so the delete is
// best effort against a backend that is still running.
//
// If the directory cannot be deleted, the extension is removed all the same and the error matches ErrDataNotRemoved
// (see DataNotRemovedError): there is nothing left to retry, the caller has to report it.
func (m *Manager) Remove(id string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if !manifest.ValidID(id) {
		return ErrNotFound
	}
	target := filepath.Join(m.opts.Dir, id)
	if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	old := filepath.Join(m.opts.Dir, fmt.Sprintf(".old-%s-%d", id, time.Now().UnixNano()))
	if err := os.Rename(target, old); err != nil {
		return err
	}
	m.removeAll(old)
	m.mu.Lock()
	delete(m.exts, id)
	m.mu.Unlock()
	m.opts.Logger.Info("extension store: removed", "id", id) // the API adds its own audit line with the user
	m.opts.OnChange(id)
	if dir := m.ExtDataDir(id); dir != "" {
		// RemoveAll does not follow a symlink, neither dir itself nor one inside: it removes the link
		if err := os.RemoveAll(dir); err != nil {
			return &DataNotRemovedError{Dir: dir, Err: err}
		}
	}
	return nil
}

// exchangeDirs atomically swaps two paths (renameat2 with RENAME_EXCHANGE). It
// is a variable so tests can simulate filesystems without support for it.
var exchangeDirs = func(a, b string) error {
	return unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
}

// replaceDir moves the directory src to target, replacing target if it exists.
// The extension is never absent from the store while it is being replaced: a
// missing target is a plain (atomic) rename, an existing one is swapped with
// src atomically, after which src holds the previous version and is deleted.
// Only on filesystems without RENAME_EXCHANGE it falls back to two renames
// with a brief window in which target does not exist.
func (m *Manager) replaceDir(src, target string) error {
	err := exchangeDirs(src, target)
	switch {
	case err == nil:
		m.removeAll(src)
		return nil
	case errors.Is(err, unix.ENOENT):
		// target does not exist (and src does, or the rename below reports
		// it): a plain rename is atomic
		return os.Rename(src, target)
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS):
		return m.replaceDirTwoStep(src, target)
	default:
		return err
	}
}

// replaceDirTwoStep is the non-atomic fallback of replaceDir.
func (m *Manager) replaceDirTwoStep(src, target string) error {
	old := ""
	if _, err := os.Lstat(target); err == nil {
		old = filepath.Join(filepath.Dir(target), fmt.Sprintf(".old-%s-%d", filepath.Base(target), time.Now().UnixNano()))
		if err := os.Rename(target, old); err != nil {
			return err
		}
	}
	if err := os.Rename(src, target); err != nil {
		if old != "" {
			if rerr := os.Rename(old, target); rerr != nil {
				m.opts.Logger.Error("cannot restore previous extension version", "old", old, "target", target, "err", rerr)
			}
		}
		return err
	}
	if old != "" {
		m.removeAll(old)
	}
	return nil
}

func copyExecutable(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
