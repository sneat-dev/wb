package hooks

import (
	"strings"
	"testing"
)

type alwaysOpenPR struct{}

func (alwaysOpenPR) OpenPullRequest(string) (bool, bool) { return true, true }

// AC: hooks-are-cheap-on-a-stream-branch — a push to a stream branch runs no
// local verification and says CI on the stream pull request is the gate.
//
// The stream branch always has an open pull request, so this only holds if the
// stream test precedes the publication test. The lookup below returns "open"
// for every branch precisely to prove that ordering.
func TestPushToAStreamBranchRunsNoLocalVerification(t *testing.T) {
	classification := ClassifyPushTier([]RefUpdate{{
		LocalRef: "refs/heads/stream/checkout", LocalSHA: "abc",
		RemoteRef: "refs/heads/stream/checkout", RemoteSHA: "def",
	}}, "main", alwaysOpenPR{})

	if classification.Tier != TierSkip {
		t.Fatalf("tier = %d, want %d (skip)", classification.Tier, TierSkip)
	}
	if classification.RunLint() || classification.IsPublication() {
		t.Fatalf("a stream-branch push ran verification: lint=%t publication=%t", classification.RunLint(), classification.IsPublication())
	}
	if !strings.Contains(classification.Reason, "stream pull request is the gate") {
		t.Errorf("reason = %q, want it to name CI on the stream pull request", classification.Reason)
	}
}

// The other half of the same AC: a push to any other branch runs the current
// full profile, unchanged.
func TestPushToANonStreamBranchKeepsTheCurrentProfile(t *testing.T) {
	classification := ClassifyPushTier([]RefUpdate{{
		LocalRef: "refs/heads/feature/x", LocalSHA: "abc",
		RemoteRef: "refs/heads/feature/x", RemoteSHA: "def",
	}}, "main", alwaysOpenPR{})
	if classification.Tier != TierPublication {
		t.Fatalf("tier = %d, want %d (publication) for a branch with an open pull request", classification.Tier, TierPublication)
	}
}

// A branch that merely mentions the word is not a stream branch: the namespace
// is a path prefix, and a substring match would silently disable verification
// on an ordinary feature branch.
func TestABranchThatMerelyMentionsStreamIsNotAStreamBranch(t *testing.T) {
	classification := ClassifyPushTier([]RefUpdate{{
		RemoteRef: "refs/heads/feature/stream-thing", LocalSHA: "abc", RemoteSHA: "def",
		LocalRef: "refs/heads/feature/stream-thing",
	}}, "main", alwaysOpenPR{})
	if classification.Tier == TierSkip {
		t.Fatalf("tier = skip; a branch merely mentioning stream disabled verification")
	}
}

// A push that mixes a stream branch with the default branch is still a
// publication push: the stream exemption never lowers the overall decision.
func TestAMixedPushKeepsTheHighestTier(t *testing.T) {
	classification := ClassifyPushTier([]RefUpdate{
		{RemoteRef: "refs/heads/stream/checkout", LocalRef: "refs/heads/stream/checkout", LocalSHA: "a", RemoteSHA: "b"},
		{RemoteRef: "refs/heads/main", LocalRef: "refs/heads/main", LocalSHA: "c", RemoteSHA: "d"},
	}, "main", nil)
	if classification.Tier != TierPublication {
		t.Fatalf("tier = %d, want %d; the stream exemption must not lower a publication push", classification.Tier, TierPublication)
	}
}

// A skip must still say why, so "fast" is never indistinguishable from "hung".
func TestASkippedPushExplainsItself(t *testing.T) {
	classification := ClassifyPushTier([]RefUpdate{{
		RemoteRef: "refs/heads/stream/x", LocalRef: "refs/heads/stream/x", LocalSHA: "a", RemoteSHA: "b",
	}}, "main", nil)
	if classification.Reason == "" || !strings.Contains(classification.Reason, "skipping lint and test") {
		t.Fatalf("reason = %q", classification.Reason)
	}
}

// REQ: commit-hook-is-fast-and-scoped — every built-in commit hook is scoped to
// the files changed in that commit and never runs a test suite.
func TestBuiltInCommitHooksAreScopedAndRunNoTests(t *testing.T) {
	for _, name := range []string{BuiltinGoPreCommit, BuiltinNodePreCommit} {
		template, ok := builtinTemplate(name)
		if !ok {
			t.Fatalf("%s has no built-in template", name)
		}
		if !strings.Contains(template, "git diff --cached --name-only") {
			t.Errorf("%s is not scoped to the files changed in this commit:\n%s", name, template)
		}
		for _, forbidden := range []string{"go test", "run test", "npm test", "pnpm test", "yarn test"} {
			if strings.Contains(template, forbidden) {
				t.Errorf("%s runs %q; a commit is a save point, not a release gate", name, forbidden)
			}
		}
	}
}

// The Node commit hook must not fail a repository for lacking a tool it did not
// install, and must not run at all where there is no package.json.
func TestNodeCommitHookIsInertWithoutAPackageManifestOrTooling(t *testing.T) {
	template, ok := builtinTemplate(BuiltinNodePreCommit)
	if !ok {
		t.Fatal("no built-in node pre-commit template")
	}
	if !strings.Contains(template, "if [ ! -f package.json ]") {
		t.Error("the node commit hook runs in a repository with no package.json")
	}
	if !strings.Contains(template, "command -v node") {
		t.Error("the node commit hook does not check that node exists before using it")
	}
	if !strings.Contains(template, "eslint.config.js") || !strings.Contains(template, ".prettierrc") {
		t.Error("the node commit hook runs its tools unconditionally rather than when they are configured")
	}
}

// The node profile must actually install the commit hook, or the scoping above
// is unreachable.
func TestNodeProfileInstallsACommitHook(t *testing.T) {
	definition, ok := builtinProfileDefinitions()["node"]
	if !ok {
		t.Fatal("no built-in node profile")
	}
	hook, ok := definition.Hooks["pre-commit"]
	if !ok || hook.Template != BuiltinNodePreCommit {
		t.Fatalf("node pre-commit = %#v, want the scoped built-in", hook)
	}
}
