package githubchecks

import (
	"testing"
)

// TestMissingRequiredChecksTrustsGitHubsOwnSkippedOrNeutralVerdict is round
// 3's replacement for the round-2 finding X2 regression test
// (sneat-dev/wb#591 red-team follow-up): the final red-team pass found the
// strict "requireExecuted" mode itself broken (on sneat-co/sneat-go, a
// required check that legitimately concludes "skipped" on an exact-tree
// reuse made the post-target wait hang forever), so round 3 removes it
// entirely. missingRequiredChecks (reverted to its pre-X2 signature and
// behavior) must treat a registered required check as satisfied by name
// regardless of its conclusion -- including "skipped" and "neutral" -- for
// every caller, deferred or not: GitHub branch protection's own evaluation
// is the gate.
func TestMissingRequiredChecksTrustsGitHubsOwnSkippedOrNeutralVerdict(t *testing.T) {
	required := []RequiredRemoteCheck{{Name: "CI"}}

	skipped := []RemoteCheck{{Name: "check-run:CI", Bucket: "pass", Conclusion: "skipped"}}
	if missing := missingRequiredChecks(skipped, required); len(missing) != 0 {
		t.Fatalf("a registered skipped required check was treated as missing: %v", missing)
	}

	neutral := []RemoteCheck{{Name: "check-run:CI", Bucket: "pass", Conclusion: "neutral"}}
	if missing := missingRequiredChecks(neutral, required); len(missing) != 0 {
		t.Fatalf("a registered neutral required check was treated as missing: %v", missing)
	}

	success := []RemoteCheck{{Name: "check-run:CI", Bucket: "pass", Conclusion: "success"}}
	if missing := missingRequiredChecks(success, required); len(missing) != 0 {
		t.Fatalf("a genuinely successful required check was treated as missing: %v", missing)
	}

	unregistered := []RemoteCheck{{Name: "check-run:OtherCheck", Bucket: "pass", Conclusion: "success"}}
	if missing := missingRequiredChecks(unregistered, required); len(missing) == 0 {
		t.Fatal("a required check that never registered under its own name was not reported missing")
	}
}

// TestCheckRunBucketTreatsNeutralAsPass is Minor 11's regression test
// (sneat-dev/wb#591 round 3 red-team follow-up): round 2's global
// neutral-joins-skipped-in-"skipping" bucket change altered `wb ci wait`
// JSON output and graduation's validateCIWait as an unintended side effect.
// Round 3 reverts it: "neutral" buckets as "pass" again, exactly as before
// round 2, while "skipped" alone still buckets as "skipping".
func TestCheckRunBucketTreatsNeutralAsPass(t *testing.T) {
	if bucket := checkRunBucket("completed", "neutral"); bucket != "pass" {
		t.Fatalf("checkRunBucket(completed, neutral) = %q, want pass", bucket)
	}
	if bucket := checkRunBucket("completed", "skipped"); bucket != "skipping" {
		t.Fatalf("checkRunBucket(completed, skipped) = %q, want skipping", bucket)
	}
	if bucket := checkRunBucket("completed", "success"); bucket != "pass" {
		t.Fatalf("checkRunBucket(completed, success) = %q, want pass", bucket)
	}
}
