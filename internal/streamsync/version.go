package streamsync

import (
	"strconv"
	"strings"
)

// below reports whether required is strictly lower than target.
//
// This one comparison is what makes `wb stream sync` idempotent against
// Renovate. Sync rebases first, so a bump Renovate already landed is present in
// the tree; comparing after the rebase finds the required version already at
// target and writes nothing. Getting this wrong in either direction is a real
// defect: too eager writes a duplicate bump on top of one that landed, too lax
// silently skips a bump that was needed.
//
// A version WB cannot parse returns false — no commit — because writing a bump
// on evidence it could not read is the more damaging mistake: it would rewrite
// a manifest against a comparison that never happened.
func below(required, target string) bool {
	requiredParts, requiredOK := semver(required)
	targetParts, targetOK := semver(target)
	if !requiredOK || !targetOK {
		return false
	}
	for index := 0; index < 3; index++ {
		if requiredParts[index] == targetParts[index] {
			continue
		}
		return requiredParts[index] < targetParts[index]
	}
	// Equal on the release triple. A prerelease is below its own release
	// (v1.2.0-rc.1 < v1.2.0); two prereleases are not compared further, since
	// a stream target is a released version.
	requiredPre := prerelease(required)
	targetPre := prerelease(target)
	return requiredPre != "" && targetPre == ""
}

// semver reads the release triple, tolerating a leading v and a range prefix.
func semver(value string) ([3]int, bool) {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimLeft(trimmed, "^~>=< ")
	trimmed = strings.TrimPrefix(trimmed, "v")
	if index := strings.IndexAny(trimmed, "-+"); index >= 0 {
		trimmed = trimmed[:index]
	}
	fields := strings.Split(trimmed, ".")
	if len(fields) < 3 {
		return [3]int{}, false
	}
	var parts [3]int
	for index := 0; index < 3; index++ {
		number, err := strconv.Atoi(fields[index])
		if err != nil || number < 0 {
			return [3]int{}, false
		}
		parts[index] = number
	}
	return parts, true
}

func prerelease(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimLeft(trimmed, "^~>=< ")
	trimmed = strings.TrimPrefix(trimmed, "v")
	if index := strings.Index(trimmed, "-"); index >= 0 {
		return trimmed[index+1:]
	}
	return ""
}

// comparable reports whether both versions can be read at all.
//
// It exists so an unreadable version is reported as UNREADABLE rather than as
// "already at target": no commit is written either way, but the two say very
// different things to an operator, and only one of them is true.
func comparable(required, target string) bool {
	_, requiredOK := semver(required)
	_, targetOK := semver(target)
	return requiredOK && targetOK
}
