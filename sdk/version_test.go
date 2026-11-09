package sdk

import (
	"runtime/debug"
	"testing"
)

func TestVersionFromBuildInfo(t *testing.T) {
	const sdkPath = "github.com/Pukka922/edelweiss/sdk"
	// extension returns the build info of an extension binary (main module example.com/ext) that has these modules
	// among its dependencies, besides Fiber.
	extension := func(deps ...*debug.Module) *debug.BuildInfo {
		return &debug.BuildInfo{
			Main: debug.Module{Path: "example.com/ext", Version: "(devel)"},
			Deps: append([]*debug.Module{{Path: "github.com/gofiber/fiber/v3", Version: "v3.5.0"}}, deps...),
		}
	}
	// replaced is the SDK dependency at v0.2.0 (the version go.mod requires), replaced by r.
	replaced := func(r debug.Module) *debug.Module {
		return &debug.Module{Path: sdkPath, Version: "v0.2.0", Replace: &r}
	}
	tests := map[string]struct {
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		"no build info":               {nil, false, "0.0.0-dev"},
		"no build info, but ok":       {nil, true, "0.0.0-dev"},
		"build info that is not ok":   {extension(&debug.Module{Path: sdkPath, Version: "v0.2.0"}), false, "0.0.0-dev"},
		"a release of the SDK":        {extension(&debug.Module{Path: sdkPath, Version: "v0.2.0"}), true, "0.2.0"},
		"a pre-release of the SDK":    {extension(&debug.Module{Path: sdkPath, Version: "v0.3.0-rc.1"}), true, "0.3.0-rc.1"},
		"a pseudo-version (as it is)": {extension(&debug.Module{Path: sdkPath, Version: "v0.2.1-0.20261105101500-eb772f4abcde"}), true, "0.2.1-0.20261105101500-eb772f4abcde"},
		"(devel): a go.work use":      {extension(&debug.Module{Path: sdkPath, Version: "(devel)"}), true, "0.0.0-dev"},
		"an empty version":            {extension(&debug.Module{Path: sdkPath}), true, "0.0.0-dev"},
		"the SDK is not a dependency": {extension(), true, "0.0.0-dev"},
		"only a module named like it": {extension(&debug.Module{Path: sdkPath + "/v2", Version: "v2.0.0"}), true, "0.0.0-dev"},
		"a module of the same repo":   {extension(&debug.Module{Path: "github.com/Pukka922/edelweiss/core", Version: "v0.2.0"}), true, "0.0.0-dev"},
		"a replace onto a local directory (make dev-sdk)": {
			extension(replaced(debug.Module{Path: "/home/me/edelweiss/sdk", Version: "(devel)"})), true, "0.0.0-dev"},
		"a replace onto a relative directory without a version": {
			extension(replaced(debug.Module{Path: "../edelweiss/sdk"})), true, "0.0.0-dev"},
		"a replace onto another version": {
			extension(replaced(debug.Module{Path: sdkPath, Version: "v0.1.0"})), true, "0.1.0"},
		"a replace onto a fork": {
			extension(replaced(debug.Module{Path: "github.com/someone/edelweiss/sdk", Version: "v0.2.1-fork.1"})), true, "0.2.1-fork.1"},
		"a replace onto a pseudo-version (as it is)": {
			extension(replaced(debug.Module{Path: sdkPath, Version: "v0.2.1-0.20261105101500-eb772f4abcde"})), true, "0.2.1-0.20261105101500-eb772f4abcde"},
		"the SDK is the main module": {
			&debug.BuildInfo{Main: debug.Module{Path: sdkPath, Version: "v0.3.0"}}, true, "0.3.0"},
		"the SDK is the main module, (devel)": {
			&debug.BuildInfo{Main: debug.Module{Path: sdkPath, Version: "(devel)"}}, true, "0.0.0-dev"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := versionFromBuildInfo(tc.info, tc.ok); got != tc.want {
				t.Fatalf("versionFromBuildInfo = %q, want %q", got, tc.want)
			}
		})
	}
}

// Version is what versionFromBuildInfo makes of the build info of the running binary.
func TestVersionIsTheBuildInfoVersion(t *testing.T) {
	if want := versionFromBuildInfo(debug.ReadBuildInfo()); Version != want {
		t.Fatalf("Version = %q, want %q", Version, want)
	}
}
