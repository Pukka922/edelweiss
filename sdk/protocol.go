package sdk

// Contract between edelweissd and extension binaries.
const (
	// APIVersion is the extension contract version implemented by this SDK.
	APIVersion = 1
	// Version is the SDK version, reported as sdkVersion in the manifest.
	Version = "0.1.0"

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
