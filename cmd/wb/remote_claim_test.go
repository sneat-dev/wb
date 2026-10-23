package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// secondMachine builds a second fixture that shares f's origin (the same
// remote store) but has its own projects root, config, and machine name —
// mirroring publishTwo's inline pattern in remote_test.go, so claim tests
// can put two independent holders against one store.

// pushCorruptClaim writes an unreadable claims/<task>.yaml directly into the
// store, through a fresh clone, so a Claim/Claims call encounters a
// decode error without going through this package's Claim/Publish paths.

// TestHolderStaleHonoursLastSeen is the production regression this whole
// feature exists for: a holder whose PUBLISHED snapshot is old but who has
// been actively claiming/refreshing through the store recently must not be
// treated as stale. bob publishes 48h stale, then claims task-7 "now" —
// that claim stamps bob's own snapshot's last_seen_at to "now" in the same
// commit — so bob's effective heartbeat is fresh even though published_at
// stays 48h old. alice's --take-over must therefore be refused exactly like
// an ordinary fresh-claim refusal, not silently allowed through stale
// published_at.

// TestTakeOverAllowedWhenBothOld proves the effective-heartbeat switch does
// not loosen take-over eligibility when there has been no recent claim
// activity at all: with both published_at and last_seen_at old, the holder
// is still judged stale and --take-over still succeeds.

// TestRemoteStatusMachineFilterTextClaimsNotDuplicated proves the fix for
// the reviewer's bug: `claimsForJSON := claimRowsAll[:0]` inside the
// --machine branch used to compact in place, corrupting claimRowsAll (which
// the TEXT-mode renderer reads unfiltered, doing its own per-machine
// filtering). With bob holding a-task and alice holding b-task, a
// --machine alice/laptop TEXT run used to render alice's section as
// "remote claims: b-task, b-task" instead of "remote claims: b-task".

// TestPersistentFlagMatrixRemoteClaimCommands extends the remote command
// allowlist coverage in remote_test.go: the three claim commands accept
// --projects-root only (to locate the state-repo clone), never --filter or
// --org (they operate on the remote store, not a local repo scan).
func TestPersistentFlagMatrixRemoteClaimCommands(t *testing.T) {
	for _, cmd := range []string{"remote claim", "remote release", "remote claims"} {
		if !persistentFlagSupport["projects-root"][cmd] {
			t.Errorf("%s must accept --projects-root: it locates the state-repo clone", cmd)
		}
		if persistentFlagSupport["filter"][cmd] {
			t.Errorf("%s must reject --filter: it does not scan the local fleet", cmd)
		}
		if persistentFlagSupport["org"][cmd] {
			t.Errorf("%s must reject --org: it operates on the remote store, not GitHub-listed repos", cmd)
		}
	}
}

// unreachableRemoteDeps builds deps whose provider points at a nonexistent
// clone URL, so any Claim/Release call fails the way an offline machine or
// a dead store would — without any network access, since the failure
// happens at `git clone`, before anything is even attempted over the wire.

// TestTryAutoReleaseFailedWhenUnreachable is wb#321's leak path: a store the
// provider genuinely cannot write to (here, a clone URL that does not
// exist, so ensureClone fails before it ever learns whether the claim was
// ours) must be reported distinctly from the advisory "skipped" outcomes —
// disabled, login trouble, noop, held by another machine — none of which
// can leave a real claim standing. Outcome must be "failed", Leaked() must
// be true, and the printed line must name the task and say "FAILED", not
// "skipped", so an operator (or a batch driver) can tell this apart from
// ordinary advisory output by grepping.

func TestWorktreeCreateHasNoClaimFlag(t *testing.T) {
	flag := newWorktreeCreateCmd(&invocation{}).Flags().Lookup("no-claim")
	if flag == nil {
		t.Fatal("newWorktreeCreateCmd() has no --no-claim flag")
	}
	if flag.DefValue != "false" {
		t.Fatalf("--no-claim default = %q, want false", flag.DefValue)
	}
}

// TestWorktreeCreateAutoClaimWiring exercises the extracted `worktree
// create` hook directly: the RunE calls tryAutoClaim exactly when a
// `remote:` config is present and --no-claim was not passed.
func TestWorktreeCreateAutoClaimWiring(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)

	var out bytes.Buffer
	result := worktreeCreateAutoClaim(f.deps("alice", at), false, f.projectsRoot, "task-7", &out)
	if result.Outcome != "acquired" {
		t.Fatalf("result = %+v, want acquired (config present, --no-claim absent)", result)
	}
	if !strings.Contains(out.String(), "remote claim: acquired task-7") {
		t.Fatalf("out = %q", out.String())
	}

	out.Reset()
	result = worktreeCreateAutoClaim(f.deps("alice", at), true, f.projectsRoot, "task-9", &out)
	if result.Outcome != "disabled" {
		t.Fatalf("result = %+v, want disabled when --no-claim is set", result)
	}
	if out.String() != "" {
		t.Fatalf("out = %q, want silence when --no-claim is set", out.String())
	}
	if files := remoteGit(t, f.origin, "ls-tree", "-r", "--name-only", "main"); strings.Contains(files, "claims/task-9.yaml") {
		t.Fatalf("--no-claim must skip the attempt entirely, not just the message: %s", files)
	}

	out.Reset()
	unconfigured := defaultRemoteDeps()
	unconfigured.configPath = filepath.Join(t.TempDir(), "none.yaml")
	result = worktreeCreateAutoClaim(unconfigured, false, t.TempDir(), "task-11", &out)
	if result.Outcome != "disabled" {
		t.Fatalf("result = %+v, want disabled when unconfigured", result)
	}
}

// TestWorktreeCreateCLINoClaimKeepsPlainJSONArray drives the whole verb
// through run(), the same entry point main() uses, proving --no-claim
// actually threads from the flag into the RunE and that its JSON output
// stays the plain worktree-results array (the "disabled" shape) rather than
// the remote_claim wrapper. A full end-to-end exercise of the wrapper shape
// itself would need a reachable `remote:` store (git@github.com in
// production, since openRemote hardcodes the SSH URL pattern), which is not
// hermetic here; TestWorktreeCreateJSONAttemptedShapeWrapsResult above pins
// that shape directly instead.
func TestWorktreeCreateCLINoClaimKeepsPlainJSONArray(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "fakehome"))
	prompt := writeOriginalPromptFixture(t, "no-claim original request")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--projects-root", projects, "worktree", "create", "cli-no-claim", "acme/app",
		"--model", "unknown", "--original-prompt-file", prompt, "--no-claim", "--format", "json",
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("worktree create --no-claim failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var results []worktrees.CreateResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("--no-claim json must stay the plain array shape: %v: %s", err, stdout.String())
	}
	if len(results) != 1 || results[0].Repository != "acme/app" {
		t.Fatalf("results = %+v", results)
	}
}

// TestWorktreeAbortCLIDiscardedRunsAutoReleaseWithoutBreakingSuccess drives
// `worktree abort --apply --disposition discarded` through run(), proving
// the new tryAutoRelease call site added to that RunE does not disturb the
// command's own success path. HOME is isolated to a directory with no
// `remote:` config, so tryAutoRelease itself takes its already-unit-tested
// "disabled" branch (TestTryAutoReleaseDisabledWithoutConfig covers its
// behavior directly); reaching its "released"/"skipped" branches through
// this CLI entry point would need a reachable remote store, which — like
// worktree create's JSON wrapper shape above — is not hermetic here.
func TestWorktreeAbortCLIDiscardedRunsAutoReleaseWithoutBreakingSuccess(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "fakehome"))
	prompt := writeOriginalPromptFixture(t, "abort discarded original request")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "cli-discard", "acme/app", "--model", "unknown", "--original-prompt-file", prompt}, &stdout, &stderr); code != exitOK {
		t.Fatalf("worktree create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"--projects-root", projects, "worktree", "abort", "cli-discard", "--apply", "--disposition", "discarded", "--remote"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("worktree abort --apply discarded failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

// TestRemoteClaimWriterAlwaysUsesStderr pins the contract that remote-claim
// notes are diagnostics, not results. They previously went to stdout in text
// format, which padded a text result with unrelated lines and, worse, left
// output on stdout from a command that had failed.
func TestRemoteClaimWriterAlwaysUsesStderr(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})

			if writer := remoteClaimWriter(cmd); writer != cmd.ErrOrStderr() {
				t.Errorf("format=%q: remote-claim notes must go to stderr", format)
			}
		})
	}
}

// TestTryAutoReleaseNilHolder tests that tryAutoRelease gracefully handles
// a ReleaseHeldByOther outcome with a nil holder (outcome.Current == nil).
// Instead of dereferencing nil, it should print "held by another machine"
// without holder detail.
