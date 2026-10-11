// Package manifest defines the edelweiss extension manifest shared by the SDK
// and edelweissd.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Localized maps a language code ("en", "de") to a text.
type Localized map[string]string

// Menu describes the navigation entry of an extension.
type Menu struct {
	Section string `json:"section"`
	Order   int    `json:"order"`
	Icon    string `json:"icon,omitempty"`
}

// UI describes the embedded frontend; all paths are relative to the UI root.
type UI struct {
	Entry   string   `json:"entry"`
	Styles  []string `json:"styles,omitempty"`
	Locales string   `json:"locales,omitempty"`
}

// Requires lists what an extension needs on the host besides edelweissd.
type Requires struct {
	// APK names Alpine packages, or names they provide (cmd:docker, so:libcrypto.so.3), that must be installed.
	APK []string `json:"apk,omitempty"`
}

// Manifest is the metadata of an extension.
type Manifest struct {
	ID            string    `json:"id"`
	Version       string    `json:"version"`
	APIVersion    int       `json:"apiVersion"`
	Name          Localized `json:"name"`
	Description   Localized `json:"description,omitempty"`
	Menu          Menu      `json:"menu"`
	UI            *UI       `json:"ui,omitempty"`
	RequiresAdmin bool      `json:"requiresAdmin"`
	Requires      *Requires `json:"requires,omitempty"`
	SDKVersion    string    `json:"sdkVersion,omitempty"`
	IconSVG       string    `json:"iconSvg,omitempty"`
}

// Sections are the allowed menu sections.
var Sections = []string{"system", "storage", "containers", "network", "tools", "other"}

// SupportedAPIVersions lists the contract versions this code understands.
var SupportedAPIVersions = []int{1}

// MaxRequiredPackages is the most entries requires.apk may have.
const MaxRequiredPackages = 32

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,31}$`)
	versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+([-+][0-9A-Za-z.-]+)?$`)
	// apkNamePattern matches an apk package name or a name a package provides; no version constraints.
	apkNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._:-]{0,127}$`)
)

// ReservedIDs are ids no extension may use: "shell" and "sdk" are the i18n namespaces of the shell and the SDK, an
// extension with such an id would overwrite their dictionaries.
var ReservedIDs = []string{"shell", "sdk"}

// ValidID reports whether id is a valid extension id: it has the right shape and is not reserved.
func ValidID(id string) bool { return idPattern.MatchString(id) && !slices.Contains(ReservedIDs, id) }

// IsSupportedAPIVersion reports whether v is a supported contract version.
func IsSupportedAPIVersion(v int) bool { return slices.Contains(SupportedAPIVersions, v) }

// RequiredAPK returns the Alpine packages the extension requires (nil if none).
func (m *Manifest) RequiredAPK() []string {
	if m.Requires == nil {
		return nil
	}
	return m.Requires.APK
}

// Parse decodes and validates a manifest.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: invalid JSON: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks the structural rules of a manifest.
func (m *Manifest) Validate() error {
	var errs []error
	add := func(field, msg string) { errs = append(errs, fmt.Errorf("manifest: %s: %s", field, msg)) }

	switch {
	case !idPattern.MatchString(m.ID):
		add("id", "must match ^[a-z0-9][a-z0-9-]{1,31}$")
	case !ValidID(m.ID):
		add("id", fmt.Sprintf("%q is reserved (reserved ids: %s)", m.ID, strings.Join(ReservedIDs, ", ")))
	}
	if !versionPattern.MatchString(m.Version) {
		add("version", "must be a semantic version like 1.2.3")
	}
	if m.APIVersion < 1 {
		add("apiVersion", "must be >= 1")
	}
	if strings.TrimSpace(m.Name["en"]) == "" {
		add("name.en", "is required")
	}
	if !slices.Contains(Sections, m.Menu.Section) {
		add("menu.section", "must be one of "+strings.Join(Sections, ", "))
	}
	if m.Menu.Icon != "" && !validRelPath(m.Menu.Icon) {
		add("menu.icon", "must be a relative path inside the UI")
	}
	if m.UI != nil {
		if !validRelPath(m.UI.Entry) {
			add("ui.entry", "must be a non-empty relative path inside the UI")
		}
		for _, s := range m.UI.Styles {
			if !validRelPath(s) {
				add("ui.styles", "must contain relative paths inside the UI")
				break
			}
		}
		if m.UI.Locales != "" && !validRelPath(m.UI.Locales) {
			add("ui.locales", "must be a relative path inside the UI")
		}
	}
	if m.Requires != nil {
		if len(m.Requires.APK) > MaxRequiredPackages {
			add("requires.apk", fmt.Sprintf("must not list more than %d packages", MaxRequiredPackages))
		}
		seen := map[string]bool{}
		for _, p := range m.Requires.APK {
			switch {
			case !apkNamePattern.MatchString(p):
				add("requires.apk", fmt.Sprintf("%q is not an apk package name (letters, digits and + . _ : -, no version constraint)", p))
			case seen[p]:
				add("requires.apk", fmt.Sprintf("%q is listed twice", p))
			}
			seen[p] = true
		}
	}
	return errors.Join(errs...)
}

func validRelPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return false
	}
	clean := path.Clean(p)
	return clean == p && clean != "." && !strings.HasPrefix(clean, "../") && clean != ".."
}
