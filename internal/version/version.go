// Package version reports which build a binary is.
package version

import "runtime/debug"

// release is set at link time by a release build:
//
//	go build -ldflags "-X github.com/bespinian/keera-chat/internal/version.release=v0.1.0"
var release string

// String is the release, or "devel" with the commit when built from a
// checkout.
func String() string {
	if release != "" {
		return release
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "devel"
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		rev += "-dirty"
	}
	return "devel+" + rev
}
