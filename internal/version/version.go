package version

// Version is set at build time via:
//
//	-ldflags "-X github.com/go-i2p/i2p-vanitygen/internal/version.Version=v1.0.0"
//
// Defaults to "dev" for local/untagged builds.
var Version = "dev"
