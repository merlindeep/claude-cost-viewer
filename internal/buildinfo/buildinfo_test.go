package buildinfo

import (
	"runtime"
	"strings"
	"testing"
)

func TestGet(t *testing.T) {
	info := Get()
	if info.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, want %q", info.GoVersion, runtime.Version())
	}
	if info.OS != runtime.GOOS || info.Arch != runtime.GOARCH {
		t.Errorf("OS/Arch = %s/%s, want %s/%s", info.OS, info.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if info.Version == "" {
		t.Error("Version should not be empty")
	}
}

func TestGetOverriddenVersion(t *testing.T) {
	// When Version is set via -ldflags, Get must return it verbatim rather than
	// consulting the embedded build info.
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "1.2.3"
	if got := Get().Version; got != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", got)
	}
}

func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		read    func() (string, bool)
		want    string
	}{
		{"ldflags version wins", "2.0.0", func() (string, bool) { return "ignored", true }, "2.0.0"},
		{"dev recovers from build info", "dev", func() (string, bool) { return "v1.5.0", true }, "v1.5.0"},
		{"dev with devel placeholder", "dev", func() (string, bool) { return "(devel)", true }, "dev"},
		{"dev with empty build info", "dev", func() (string, bool) { return "", true }, "dev"},
		{"dev with no build info", "dev", func() (string, bool) { return "", false }, "dev"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.version, tc.read); got != tc.want {
				t.Errorf("resolveVersion(%q) = %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}

func TestMainVersion(t *testing.T) {
	// In a test binary build info is available, so this returns ok==true.
	if _, ok := mainVersion(); !ok {
		t.Error("expected build info to be available in the test binary")
	}
}

func TestInfoString(t *testing.T) {
	s := Info{Version: "1.0.0", Commit: "abc123", Date: "2026-01-01", GoVersion: "go1.25", OS: "darwin", Arch: "arm64"}.String()
	for _, want := range []string{"ccview", "1.0.0", "abc123", "2026-01-01", "darwin/arm64"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}
