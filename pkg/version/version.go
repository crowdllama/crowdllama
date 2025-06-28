// Package version provides version information for CrowdLlama applications.
package version

import (
	"fmt"
	"runtime"
)

var (
	// Version is the version of the application
	Version = "dev"
	// CommitHash is the git commit hash
	CommitHash = "unknown"
	// BuildDate is the build date
	BuildDate = "unknown"
	// GoVersion is the Go version used to build
	GoVersion = runtime.Version()
)

// Info contains version information
type Info struct {
	Version    string `json:"version"`
	CommitHash string `json:"commit_hash"`
	BuildDate  string `json:"build_date"`
	GoVersion  string `json:"go_version"`
}

// GetInfo returns the version information
func GetInfo() Info {
	return Info{
		Version:    Version,
		CommitHash: CommitHash,
		BuildDate:  BuildDate,
		GoVersion:  GoVersion,
	}
}

// String returns a formatted version string
func String() string {
	return fmt.Sprintf("CrowdLlama %s (commit: %s, built: %s, go: %s)",
		Version, CommitHash, BuildDate, GoVersion)
}

// Short returns a short version string
func Short() string {
	return fmt.Sprintf("CrowdLlama %s", Version)
}
