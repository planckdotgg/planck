// Package version is the one source of Planck's version. scripts/version.sh
// reads Version from this file, and the release workflow requires the tag
// to be v<Version>. Release builds set Commit and BuildDate with -ldflags -X.
package version

var Version = "0.1.0"

var Commit = "dev"

var BuildDate = ""

// OfficialBuild is "1" in release builds (-ldflags -X ...version.OfficialBuild=1):
// their telemetry counts as a release, not as test signals.
var OfficialBuild = ""

// IsOfficialBuild reports whether this is a release build.
func IsOfficialBuild() bool { return OfficialBuild == "1" }

// String is the version alone, for the interface.
func String() string { return Version }

// Full is the version with the commit and build date it was built from.
func Full() string {
	s := Version + " (" + Commit
	if BuildDate != "" {
		s += ", " + BuildDate
	}
	return s + ")"
}
