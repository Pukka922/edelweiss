// Package version holds the build version of edelweissd, set via -ldflags.
package version

// Version is overridden at build time:
// -ldflags "-X github.com/Pukka922/edelweiss/core/internal/version.Version=0.1.0"
var Version = "0.1.0-dev"
