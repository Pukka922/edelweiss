package sdk

import "runtime/debug"

// Version is the SDK version, reported as sdkVersion in the manifest. The source holds no version: it is the version
// of the module github.com/Pukka922/edelweiss/sdk that the extension binary was built with, from the binary's build
// info (runtime/debug.ReadBuildInfo), without the leading v. A replace onto another module version reports that
// version, and a pseudo-version is reported as it is. A build that records no usable version, such as one against a
// local checkout of the SDK (a go.work replace or use, as make dev-sdk of an extension sets up), reports "0.0.0-dev".
var Version = versionFromBuildInfo(debug.ReadBuildInfo())

// Contract between edelweissd and extension binaries.
const (
	// APIVersion is the extension contract version implemented by this SDK.
	APIVersion = 1

	HeaderUser      = "X-Edelweiss-User"
	HeaderUID       = "X-Edelweiss-Uid"
	HeaderAdmin     = "X-Edelweiss-Admin"
	HeaderLang      = "X-Edelweiss-Lang"
	HeaderRequestID = "X-Request-Id"

	EnvListenFD    = "EDELWEISS_LISTEN_FD"
	EnvExtensionID = "EDELWEISS_EXTENSION_ID"
	EnvAPIVersion  = "EDELWEISS_API_VERSION"
	EnvDevListen   = "EDELWEISS_DEV_LISTEN"
	EnvDevUIDir    = "EDELWEISS_DEV_UI_DIR"

	InternalPrefix = "/_edelweiss"
	StatusPath     = InternalPrefix + "/status"
	UIPrefix       = InternalPrefix + "/ui"
	ManifestFlag   = "--edelweiss-manifest"
)
