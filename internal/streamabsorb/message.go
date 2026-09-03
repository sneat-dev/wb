package streamabsorb

import (
	"path"
	"strings"
)

// AggregateMessage builds the squash commit message.
//
// A squash that keeps only a title discards every commit message the branch
// carried, and `git log` on the landed branch can then no longer answer what
// the change contained. So the message aggregates: the title as subject, the
// summary as body, then one line per source commit, then the ledger reference
// that says which reviewed patch set this commit is.
//
// Implements: dependency-streams#req:the-squash-message-aggregates-the-source-commits.
func AggregateMessage(title, summary string, commits []Commit, fingerprint string) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(title))
	builder.WriteString("\n")
	if trimmed := strings.TrimSpace(summary); trimmed != "" {
		builder.WriteString("\n")
		builder.WriteString(trimmed)
		builder.WriteString("\n")
	}
	if len(commits) > 0 {
		builder.WriteString("\nSource commits:\n")
		for _, commit := range commits {
			builder.WriteString("  " + commit.Short() + " " + commit.Subject + "\n")
		}
	}
	if fingerprint != "" {
		builder.WriteString("\nReviewed-patch-set: " + shortFingerprint(fingerprint) + "\n")
	}
	return builder.String()
}

// DefaultTitle derives a subject when the caller supplies none.
//
// The FIRST source commit's subject is used deliberately: it is the one the
// author wrote when the work began, and it is what GitHub would have used
// anyway. Where that is a `wip(...)`-style placeholder the caller is told to
// pass an explicit title rather than having one invented for it.
func DefaultTitle(commits []Commit) (string, bool) {
	if len(commits) == 0 {
		return "", false
	}
	subject := strings.TrimSpace(commits[0].Subject)
	lower := strings.ToLower(subject)
	for _, placeholder := range []string{"wip", "fixup!", "squash!", "temp", "tmp"} {
		if strings.HasPrefix(lower, placeholder) {
			return subject, false
		}
	}
	return subject, true
}

// mechanicalManifests are the files a dependency bump touches and nothing else.
var mechanicalManifests = map[string]bool{
	"go.mod": true, "go.sum": true,
	"package.json": true, "package-lock.json": true,
	"pnpm-lock.yaml": true, "pnpm-workspace.yaml": true,
	"yarn.lock": true, "bun.lock": true, "bun.lockb": true,
}

// IsMechanical reports whether every touched file is a dependency manifest or
// lockfile, which is what lets a bump skip the review ledger.
//
// TODO(rows-8-9-followup): replace this with the shared classifier that lands
// with `wb pr land` (#337) once it is on main, so `absorb` and `pr land` decide
// "mechanical" from one implementation rather than two. This deliberately
// errs toward NOT mechanical — an empty file list, or any file it does not
// recognise, means the ledger still applies, because wrongly skipping a review
// is the damaging direction.
func IsMechanical(commits []Commit) bool {
	touched := 0
	for _, commit := range commits {
		for _, file := range commit.Files {
			touched++
			if !mechanicalManifests[path.Base(file)] {
				return false
			}
		}
	}
	return touched > 0
}
