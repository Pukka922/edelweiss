// Package version holds the build version of edelweissd, set via -ldflags.
package version

// Version is the placeholder 0.0.0-dev in the source; the git tag is the only source of a release's version. A build
// with a VERSION (make build VERSION=<version>, which make release runs with the version of the tag) sets it:
// -ldflags "-X github.com/Pukka922/edelweiss/core/internal/version.Version=0.2.0"
var Version = "0.0.0-dev"
