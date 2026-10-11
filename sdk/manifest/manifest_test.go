package manifest

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

const valid = `{
  "id": "system", "version": "0.1.0", "apiVersion": 1,
  "name": {"de": "Übersicht", "en": "Overview"},
  "menu": {"section": "system", "order": 10, "icon": "icon.svg"},
  "ui": {"entry": "index.js", "styles": ["style.css"], "locales": "locales"},
  "requiresAdmin": false
}`

func TestParseValid(t *testing.T) {
	m, err := Parse([]byte(valid))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.ID != "system" || m.Name["en"] != "Overview" || m.UI.Entry != "index.js" || m.Menu.Order != 10 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]struct {
		mutate func(m *Manifest)
		want   string
	}{
		"bad id":             {func(m *Manifest) { m.ID = "Bad_ID" }, "id"},
		"reserved shell":     {func(m *Manifest) { m.ID = "shell" }, `"shell" is reserved`},
		"reserved sdk":       {func(m *Manifest) { m.ID = "sdk" }, `"sdk" is reserved`},
		"bad version":        {func(m *Manifest) { m.Version = "v1" }, "version"},
		"api version":        {func(m *Manifest) { m.APIVersion = 0 }, "apiVersion"},
		"missing en name":    {func(m *Manifest) { m.Name = Localized{"de": "x"} }, "name.en"},
		"bad section":        {func(m *Manifest) { m.Menu.Section = "games" }, "menu.section"},
		"absolute entry":     {func(m *Manifest) { m.UI.Entry = "/index.js" }, "ui.entry"},
		"dotdot style":       {func(m *Manifest) { m.UI.Styles = []string{"../x.css"} }, "ui.styles"},
		"empty entry":        {func(m *Manifest) { m.UI.Entry = "" }, "ui.entry"},
		"bad icon":           {func(m *Manifest) { m.Menu.Icon = "../icon.svg" }, "menu.icon"},
		"version constraint": {func(m *Manifest) { m.Requires = &Requires{APK: []string{"docker>=27"}} }, `requires.apk: "docker>=27" is not an apk package name`},
		"package with space": {func(m *Manifest) { m.Requires = &Requires{APK: []string{"docker cli"}} }, `requires.apk: "docker cli" is not an apk package name`},
		"empty package":      {func(m *Manifest) { m.Requires = &Requires{APK: []string{""}} }, `requires.apk: "" is not an apk package name`},
		"duplicate package":  {func(m *Manifest) { m.Requires = &Requires{APK: []string{"docker", "docker"}} }, `requires.apk: "docker" is listed twice`},
		"too many packages":  {func(m *Manifest) { m.Requires = &Requires{APK: manyPackages(33)} }, "requires.apk: must not list more than 32 packages"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m, err := Parse([]byte(valid))
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(m)
			err = m.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestUIOptional(t *testing.T) {
	m, err := Parse([]byte(`{"id":"noui","version":"1.0.0","apiVersion":1,"name":{"en":"No UI"},"menu":{"section":"tools"}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.UI != nil {
		t.Fatalf("expected nil UI")
	}
}

func TestValidID(t *testing.T) {
	for _, id := range []string{"ab", "system", "docker-compose", "a1"} {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false", id)
		}
	}
	for _, id := range []string{"a", "-ab", "Ab", "a_b", strings.Repeat("a", 33), "../x"} {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true", id)
		}
	}
}

// shell and sdk are the i18n namespaces of the shell and the SDK: an extension with such an id would overwrite
// their dictionaries.
func TestReservedIDs(t *testing.T) {
	if !slices.Equal(ReservedIDs, []string{"shell", "sdk"}) {
		t.Errorf("ReservedIDs = %v", ReservedIDs)
	}
	for _, id := range ReservedIDs {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true for a reserved id", id)
		}
		data := strings.Replace(valid, `"id": "system"`, `"id": "`+id+`"`, 1)
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("Parse with id %q = %v, want a reserved error", id, err)
		}
	}
	if !ValidID("shells") || !ValidID("my-sdk") {
		t.Error("ids that merely contain a reserved id must stay valid")
	}
}

func TestSupportedAPIVersion(t *testing.T) {
	if !IsSupportedAPIVersion(1) || IsSupportedAPIVersion(2) {
		t.Fatal("unexpected supported api versions")
	}
}

func manyPackages(n int) []string {
	p := make([]string, n)
	for i := range p {
		p[i] = fmt.Sprintf("pkg%d", i)
	}
	return p
}

func TestRequires(t *testing.T) {
	with := strings.Replace(valid, `"requiresAdmin": false`,
		`"requiresAdmin": false, "requires": {"apk": ["docker", "cmd:docker", "so:libcrypto.so.3"]}`, 1)
	m, err := Parse([]byte(with))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := m.RequiredAPK(); !slices.Equal(got, []string{"docker", "cmd:docker", "so:libcrypto.so.3"}) {
		t.Fatalf("RequiredAPK() = %v", got)
	}
	m.Requires.APK = manyPackages(MaxRequiredPackages)
	if err := m.Validate(); err != nil {
		t.Fatalf("%d packages must be allowed: %v", MaxRequiredPackages, err)
	}

	plain, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if plain.RequiredAPK() != nil {
		t.Fatalf("RequiredAPK() without requires = %v, want nil", plain.RequiredAPK())
	}
	out, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "requires\"") {
		t.Fatalf("a manifest without requirements must not write requires: %s", out)
	}

	empty, err := Parse([]byte(strings.Replace(valid, `"requiresAdmin": false`, `"requiresAdmin": false, "requires": {}`, 1)))
	if err != nil {
		t.Fatalf("an empty requires object must be valid: %v", err)
	}
	if empty.RequiredAPK() != nil {
		t.Fatalf("RequiredAPK() of an empty requires = %v, want nil", empty.RequiredAPK())
	}
}
