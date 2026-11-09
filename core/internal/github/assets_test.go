package github

import "testing"

func TestParseCoreAsset(t *testing.T) {
	for _, c := range []struct{ name, ver, arch string }{
		{"edelweissd_0.3.1_linux_amd64", "0.3.1", "amd64"},
		{"edelweissd_0.3.1-rc.1_linux_arm64", "0.3.1-rc.1", "arm64"},
	} {
		v, a, ok := ParseCoreAsset(c.name)
		if !ok || v != c.ver || a != c.arch {
			t.Errorf("%s: %q %q %v", c.name, v, a, ok)
		}
	}
	for _, name := range []string{"edelweissd_v0.3.1_linux_amd64", "edelweissd_0.3.1_linux_386", "edelweissd_0.3.1_linux_amd64.tar.gz",
		"edelweiss-ext-x_0.1.0_linux_amd64", "edelweissd_01.3.1_linux_amd64", "edelweissd_0.3.1_linux_amd64\n", ""} {
		if _, _, ok := ParseCoreAsset(name); ok {
			t.Errorf("accepted %q", name)
		}
	}
}
