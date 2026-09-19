package herdr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MinimumVersion is the oldest herdr release this package's CLI surface was
// learned against and is declared to support. Update it, deliberately,
// when a later task adopts a herdr feature unavailable before some newer
// release.
const MinimumVersion = "0.9.1"

// minimumVersion is [MinimumVersion], parsed once at package init. Parsing
// this package's own constant can only fail from a bug in this package —
// never from herdr's (untrusted) output — so it is checked with a panic
// here, the same way regexp.MustCompile checks a package's own pattern,
// rather than threading an unreachable-in-practice error return through
// every caller of [Client.EnsureMinimumVersion].
var minimumVersion = mustParseVersion(MinimumVersion)

func mustParseVersion(raw string) Version {
	version, err := ParseVersion(raw)
	if err != nil {
		panic("herdr: MinimumVersion constant does not parse: " + err.Error())
	}
	return version
}

// Version is a parsed herdr release number, as `herdr --version` reports
// it ("herdr 0.9.1").
type Version struct {
	Major, Minor, Patch int
	Raw                 string
}

var versionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// ParseVersion parses herdr's `--version` output, or a bare "X.Y.Z" string
// such as [MinimumVersion]. It never panics: any input that does not
// contain a recognizable X.Y.Z number is reported as an error wrapping
// [ErrUnparseableOutput].
func ParseVersion(output string) (Version, error) {
	trimmed := strings.TrimSpace(output)
	match := versionPattern.FindStringSubmatch(trimmed)
	if match == nil {
		return Version{}, fmt.Errorf("%w: %q does not contain a herdr version number", ErrUnparseableOutput, truncate(trimmed, 256))
	}
	major, majorErr := strconv.Atoi(match[1])
	minor, minorErr := strconv.Atoi(match[2])
	patch, patchErr := strconv.Atoi(match[3])
	if majorErr != nil || minorErr != nil || patchErr != nil {
		return Version{}, fmt.Errorf("%w: %q has a version number out of int range", ErrUnparseableOutput, truncate(trimmed, 256))
	}
	return Version{Major: major, Minor: minor, Patch: patch, Raw: trimmed}, nil
}

// Less reports whether v is an older release than other, comparing
// major, then minor, then patch.
func (v Version) Less(other Version) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	return v.Patch < other.Patch
}

func (v Version) String() string {
	if v.Raw != "" {
		return v.Raw
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// truncate bounds an untrusted string before it is embedded in an error
// message, so a very large or unexpected herdr response cannot make an
// error message unbounded.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
