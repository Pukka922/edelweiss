package extmgr

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Pukka922/edelweiss/sdk/manifest"
)

// ErrMissingPackages is what a *MissingPackagesError matches with errors.Is.
var ErrMissingPackages = errors.New("missing packages")

// MissingPackagesError refuses an extension that requires Alpine packages which are not installed.
type MissingPackagesError struct {
	Packages []string // in the order of the manifest
}

func (e *MissingPackagesError) Error() string {
	return "missing packages: " + strings.Join(e.Packages, ", ")
}

// Is makes errors.Is(err, ErrMissingPackages) true.
func (e *MissingPackagesError) Is(target error) bool { return target == ErrMissingPackages }

// PackageChecker tells which of the named Alpine packages are not installed (implemented by *apkdb.DB).
type PackageChecker interface {
	Missing(names []string) ([]string, error)
}

// checkPackages fails with a *MissingPackagesError if man requires Alpine packages that are not installed.
func (m *Manager) checkPackages(man *manifest.Manifest) error {
	want := man.RequiredAPK()
	if len(want) == 0 {
		return nil
	}
	missing, err := m.opts.Packages.Missing(want)
	if err != nil {
		return fmt.Errorf("cannot check the required packages: %w", err)
	}
	if len(missing) > 0 {
		m.opts.Logger.Warn("extension needs packages that are not installed", "id", man.ID, "packages", missing)
		return &MissingPackagesError{Packages: missing}
	}
	return nil
}

// MissingPackages returns the Alpine packages e requires that are not installed; never nil. If apk's database cannot
// be read, it logs why and returns all required packages.
func (m *Manager) MissingPackages(e *Extension) []string {
	want := e.Manifest.RequiredAPK()
	if len(want) == 0 {
		return []string{}
	}
	missing, err := m.opts.Packages.Missing(want)
	if err != nil {
		m.opts.Logger.Warn("cannot check the required packages", "id", e.Manifest.ID, "err", err)
		return slices.Clone(want)
	}
	if missing == nil {
		return []string{}
	}
	return missing
}
