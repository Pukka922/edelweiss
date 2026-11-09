package sdk

import (
	"runtime/debug"
	"strings"
)

const (
	// modulePath is the module of this SDK, as the build info of a binary records it.
	modulePath = "github.com/Pukka922/edelweiss/sdk"
	// devVersion is the version of an SDK whose build records no usable version: no build info, a workspace (go.work
	// use), a replace onto a local directory (make dev-sdk in an extension) or a build of the SDK's own checkout.
	devVersion = "0.0.0-dev"
)

// versionFromBuildInfo returns the version of this SDK module that a binary was built with, without the leading v: the
// entry of the module among the dependencies, or the main module if the SDK is the main module. A replace onto another
// module version counts with that version, a replace onto a local directory (version "(devel)" or none) does not. A
// pseudo-version is returned as it is. Without a usable version it returns devVersion. info and ok are the results
// of debug.ReadBuildInfo.
func versionFromBuildInfo(info *debug.BuildInfo, ok bool) string {
	if !ok || info == nil {
		return devVersion
	}
	var mod *debug.Module
	if info.Main.Path == modulePath {
		mod = &info.Main
	} else {
		for _, dep := range info.Deps {
			if dep != nil && dep.Path == modulePath {
				mod = dep
				break
			}
		}
	}
	if mod == nil {
		return devVersion
	}
	if mod.Replace != nil {
		mod = mod.Replace
	}
	if mod.Version == "" || mod.Version == "(devel)" {
		return devVersion
	}
	return strings.TrimPrefix(mod.Version, "v")
}
