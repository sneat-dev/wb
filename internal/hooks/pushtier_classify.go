package hooks

import (
	"io"
	"os"
	"strings"

	"github.com/sneat-dev/wb/internal/console"
)

// DefaultBranchEnv lets a repository or a fleet policy assert its default
// branch explicitly, skipping local detection entirely. It never makes a
// network call either way; this is purely for the rare local checkout where
// origin/HEAD was never recorded.
const DefaultBranchEnv = "WB_DEFAULT_BRANCH"

// ClassifyPendingPush is the entry point `wb hooks push-tier` calls: it reads
// Git's pre-push ref list from stdin (unless stdin is a terminal, in which
// case there is no ref list to read and reading it would hang forever
// waiting for a human who was never asked to type anything — see
// shouldReplicateStdin for the same hazard), determines the repository's
// default branch from purely local Git state, and classifies the push.
//
// A terminal stdin (an agent or a human running the hook by hand) has no ref
// list to classify from, so it reports the tier as explicitly unknown rather
// than guessing: Tier 1, with a reason that says why, exactly like any other
// unresolved PR-status case. CI remains the real gate for a publication push
// either way.
func ClassifyPendingPush(stdin io.Reader, repoRoot string) (Classification, error) {
	if console.IsTerminal(stdin) {
		return Classification{
			Tier:   TierLint,
			Reason: "no pushed-ref list available (interactive invocation, not a real git push); running the fast lane — CI is the real gate",
		}, nil
	}
	updates, err := ParseRefUpdates(stdin)
	if err != nil {
		return Classification{}, err
	}
	defaultBranch := detectDefaultBranch(repoRoot)
	lookup := NewCachedGHPRLookup(repoRoot)
	return ClassifyPushTier(updates, defaultBranch, lookup), nil
}

// detectDefaultBranch resolves the repository's default branch using only
// local Git state: an explicit override, the recorded origin/HEAD symref (set
// by a full clone or `git remote set-head origin -a`), or, failing both, the
// first of the conventional names that already exists as a local
// remote-tracking ref. It never fetches and never contacts the network, so it
// can never be the reason a hook hangs; an unresolved default branch simply
// means the default-branch publication test never matches, not that
// classification fails.
func detectDefaultBranch(repoRoot string) string {
	if override := strings.TrimSpace(os.Getenv(DefaultBranchEnv)); override != "" {
		return override
	}
	if symref, err := gitOutput(repoRoot, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimPrefix(strings.TrimSpace(symref), "origin/"); name != "" {
			return name
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if _, err := gitOutput(repoRoot, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+candidate); err == nil {
			return candidate
		}
	}
	return ""
}
