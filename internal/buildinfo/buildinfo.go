// Package buildinfo exposes the binary's version metadata.
//
// The variables below are overridden at build time via -ldflags, for example:
//
//	go build -ldflags "-X github.com/merlindeep/claude-cost-viewer/internal/buildinfo.Version=1.2.3"
//
// GoReleaser sets all three during a release build (see .goreleaser.yaml).
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Build metadata. Defaults are used for local "go build"/"go install" builds
// where no -ldflags are supplied.
var (
	// Version is the semantic version of the build (e.g. "1.2.3").
	Version = "dev"
	// Commit is the git commit hash the binary was built from.
	Commit = "none"
	// Date is the RFC3339 build timestamp.
	Date = "unknown"
)

// Info is an immutable snapshot of the build metadata.
type Info struct {
	Version   string
	Commit    string
	Date      string
	GoVersion string
	OS        string
	Arch      string
}

// Get returns the current build information. When the binary was produced by
// "go install module@version" (so -ldflags were not applied), it tries to
// recover the module version from the embedded build info.
func Get() Info {
	return Info{
		Version:   resolveVersion(Version, mainVersion),
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

// resolveVersion prefers an -ldflags-injected version, falling back to the
// version embedded by "go install module@version".
func resolveVersion(version string, read func() (string, bool)) string {
	if version == "dev" {
		if v, ok := read(); ok && v != "" && v != "(devel)" {
			return v
		}
	}
	return version
}

// mainVersion reports the main module's version from the embedded build info.
func mainVersion() (string, bool) {
	if bi, ok := debug.ReadBuildInfo(); ok {
		return bi.Main.Version, true
	}
	return "", false
}

// String renders the build information as a single human-readable line.
func (i Info) String() string {
	return fmt.Sprintf("ccview %s (commit %s, built %s, %s %s/%s)",
		i.Version, i.Commit, i.Date, i.GoVersion, i.OS, i.Arch)
}
