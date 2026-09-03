// Package streambranch names the branch namespace a dependency stream owns.
//
// It is deliberately a leaf with no dependencies. Both the stream verbs and the
// Git hooks have to agree on what a stream branch is — the hooks decide whether
// to run local verification, the verbs decide where work lives — and the stream
// package already depends on hook health, so a shared constant defined in
// either one would be an import cycle. Duplicating the prefix in two packages
// would be the same defect one release later, when one of the copies moves.
package streambranch

import "strings"

// Prefix is the namespace every stream branch lives in: `stream/<name>`.
const Prefix = "stream/"

// Name renders the stream branch for one stream name.
func Name(stream string) string { return Prefix + stream }

// Is reports whether a branch name — or a full `refs/heads/…` ref — is inside
// the stream namespace.
//
// A branch that merely mentions the word (`feature/stream-thing`) is not one:
// the namespace is a path prefix, and treating a substring as a match would
// silently disable local verification on an ordinary feature branch.
func Is(ref string) bool {
	return strings.HasPrefix(strings.TrimPrefix(ref, "refs/heads/"), Prefix)
}

// StreamName extracts the stream name from a branch or ref, reporting false
// when the ref is not a stream branch.
func StreamName(ref string) (string, bool) {
	branch := strings.TrimPrefix(ref, "refs/heads/")
	name, found := strings.CutPrefix(branch, Prefix)
	if !found || name == "" {
		return "", false
	}
	return name, true
}
