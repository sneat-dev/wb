//go:build e2e

package worktrees

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func checkpointBoundaryOptions(fixture *gitFixture, worktree string, source session.Record) SessionCheckpointOptions {
	return SessionCheckpointOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SourceSession: source,
		TargetMachine: "target", HandoffID: "handoff-checkpoint", SuccessorWBSessionID: "wbs-successor",
		Handover: SessionHandover{Summary: "continue"}, Now: source.StartedAt.Add(time.Second),
	}
}

type checkpointGitFaultRunner struct {
	runner.Runner
	match   func([]string) bool
	rewrite func([]string) (string, bool)
	call    int
	fail    int
	out     string
	err     error
}

func (r *checkpointGitFaultRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == "git" && len(args) >= 2 && r.rewrite != nil {
		if output, ok := r.rewrite(args[2:]); ok {
			return runner.Result{CombinedOutput: output}, nil
		}
	}
	if name == "git" && len(args) >= 2 && r.match != nil && r.match(args[2:]) {
		r.call++
		if r.call == r.fail {
			return runner.Result{CombinedOutput: r.out}, r.err
		}
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

//nolint:paralleltest // newSessionCheckpointFixture configures process-wide Git and agent environment
func TestE2ESessionCheckpointPreflightGitRefusals(t *testing.T) {
	boom := errors.New("checkpoint Git query failed")
	cases := []struct {
		name, operation, want string
		match                 func([]string) bool
		fail                  int
		out                   string
		err                   error
	}{
		{"status query", "status", "inspect source worktree status", func(args []string) bool { return len(args) > 0 && args[0] == "status" }, 1, "", boom},
		{"invalid source HEAD", "head", "resolve exact source work commit", func(args []string) bool {
			return len(args) > 0 && args[0] == "rev-parse" && strings.Contains(strings.Join(args, " "), "HEAD^{commit}")
		}, 1, "not-a-commit\n", nil},
		{"named branch disagrees with guard", "guard-branch", "managed worktree branch", func(args []string) bool { return len(args) > 1 && args[0] == "branch" && args[1] == "--show-current" }, 1, "wrong-branch\n", nil},
		{"branch changes after dry run", "branch", "source named branch changed during preflight", func(args []string) bool { return len(args) > 0 && args[0] == "symbolic-ref" }, 3, "wrong-branch\n", nil},
		{"branch changes after request validation", "branch-later", "source named branch changed during preflight", func(args []string) bool { return len(args) > 0 && args[0] == "symbolic-ref" }, 4, "wrong-branch\n", nil},
		{"status changes after dry run", "status-later", "source worktree changed during preflight", func(args []string) bool { return len(args) > 0 && args[0] == "status" }, 2, "dirty.txt\n", nil},
	}
	for _, tc := range cases {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(tc.name, func(t *testing.T) {
			fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-preflight-"+tc.operation)
			options := SessionCheckpointOptions{
				ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SourceSession: source,
				TargetMachine: "target", HandoffID: "handoff-preflight", SuccessorWBSessionID: "wbs-successor",
				Handover: SessionHandover{Summary: "continue"}, Now: source.StartedAt.Add(time.Second),
			}
			r := &checkpointGitFaultRunner{Runner: runner.New(), match: tc.match, fail: tc.fail, out: tc.out, err: tc.err}
			before := gitTestOutput(t, worktree, "rev-parse", "HEAD")
			_, err := CreateSessionCheckpoint(withGitRunner(context.Background(), r), options)
			if r.call < tc.fail || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("preflight error = %v, fault calls = %d, want %q on call %d", err, r.call, tc.want, tc.fail)
			}
			if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != before {
				t.Fatalf("source HEAD changed from %s to %s", before, got)
			}
			if _, statErr := os.Stat(filepath.Join(fixture.home, sessionmove.DirName, options.HandoffID)); !os.IsNotExist(statErr) {
				t.Fatalf("checkpoint was admitted before preflight refusal: %v", statErr)
			}
		})
	}
}

//nolint:paralleltest // newSessionCheckpointFixture configures process-wide Git and agent environment
func TestE2ESessionCheckpointInvalidIdentityIsNotAdmitted(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-invalid-identity")
	options := SessionCheckpointOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, SourceSession: source,
		TargetMachine: "target", HandoffID: "bad/id", SuccessorWBSessionID: "wbs-successor",
		Handover: SessionHandover{Summary: "continue"}, Now: source.StartedAt.Add(time.Second),
	}
	_, err := CreateSessionCheckpoint(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "validate source checkpoint identity") {
		t.Fatalf("invalid identity error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.home, sessionmove.DirName, "bad")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid handoff created private state: %v", statErr)
	}
}

//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
func TestE2ESessionCheckpointManagedAndClaimBranchRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, *gitFixture, string, *SessionCheckpointOptions)
	}{
		{"wrong managed root", "managed Git worktree", func(t *testing.T, _ *gitFixture, _ string, options *SessionCheckpointOptions) {
			options.ProjectsRoot = filepath.Join(t.TempDir(), "other-projects")
		}},
	} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(tc.name, func(t *testing.T) {
			fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-managed-"+strings.ReplaceAll(tc.name, " ", "-"))
			options := checkpointBoundaryOptions(fixture, worktree, source)
			tc.change(t, fixture, worktree, &options)
			_, err := CreateSessionCheckpoint(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("managed/claim refusal = %v, want %q", err, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(fixture.home, sessionmove.DirName, options.HandoffID)); !os.IsNotExist(statErr) {
				t.Fatalf("refusal admitted handoff: %v", statErr)
			}
		})
	}
}

//nolint:paralleltest // newSessionCheckpointFixture configures process-wide Git and agent environment
func TestE2ESessionCheckpointRefusesClaimThatDisagreesWithQueriedBranch(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-claim-branch-query")
	options := checkpointBoundaryOptions(fixture, worktree, source)
	symbolicCalls := 0
	r := &checkpointGitFaultRunner{Runner: runner.New(), rewrite: func(args []string) (string, bool) {
		if len(args) > 0 && args[0] == "symbolic-ref" {
			symbolicCalls++
			if symbolicCalls == 1 {
				return "other-branch\n", true
			}
		}
		if len(args) > 1 && args[0] == "branch" && args[1] == "--show-current" {
			return "other-branch\n", true
		}
		return "", false
	}}
	_, err := CreateSessionCheckpoint(withGitRunner(context.Background(), r), options)
	if err == nil || !strings.Contains(err.Error(), "active Work Log branch") {
		t.Fatalf("claim/queried-branch disagreement = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.home, sessionmove.DirName, options.HandoffID)); !os.IsNotExist(statErr) {
		t.Fatalf("claim mismatch was admitted: %v", statErr)
	}
}

//nolint:paralleltest // newSessionCheckpointFixture configures process-wide Git and agent environment
func TestE2ESessionCheckpointGeneratedIDsAndExactAdmission(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-generated-ids")
	options := checkpointBoundaryOptions(fixture, worktree, source)
	options.HandoffID, options.SuccessorWBSessionID = "", ""
	options.Now = time.Time{}
	result, err := CreateSessionCheckpoint(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Request.HandoffID == "" || result.Request.SuccessorWBSessionID == "" || result.Request.CreatedAt.IsZero() {
		t.Fatalf("generated checkpoint identity = %#v", result.Request)
	}
	if result.Digest != sessionmove.DigestBytes(result.RequestBytes) {
		t.Fatalf("admitted digest %s does not match request bytes", result.Digest)
	}
	stored, err := sessionmove.NewStore(filepath.Join(fixture.home, sessionmove.DirName)).Load(result.Request.HandoffID)
	if err != nil || stored.Digest != result.Digest || stored.Request != result.Request {
		t.Fatalf("durable checkpoint = (%#v, %v), want exact generated identity", stored, err)
	}
}

//nolint:paralleltest // newSessionCheckpointFixture configures process-wide Git and agent environment
func TestE2ESessionCheckpointDescriptorAndHomeFaultBoundaries(t *testing.T) {
	injected := errors.New("checkpoint boundary failed")
	for _, tc := range []struct {
		name, want string
		set        func(*sessionCheckpointPorts, **os.File)
	}{
		{"canonical opener", "open managed canonical repository", func(ports *sessionCheckpointPorts, _ **os.File) {
			ports.openCanonical = func(string) (*canonicalRepository, error) { return nil, injected }
		}},
		{"worktree opener", "hold source worktree", func(ports *sessionCheckpointPorts, held **os.File) {
			original := ports.openCanonical
			ports.openCanonical = func(path string) (*canonicalRepository, error) {
				canonical, err := original(path)
				if err == nil {
					*held = canonical.root
				}
				return canonical, err
			}
			ports.openWorktree = func(string) (*cleanupWorktreeHandle, error) { return nil, injected }
		}},
		{"WB home", "checkpoint boundary failed", func(ports *sessionCheckpointPorts, held **os.File) {
			original := ports.openCanonical
			ports.openCanonical = func(path string) (*canonicalRepository, error) {
				canonical, err := original(path)
				if err == nil {
					*held = canonical.root
				}
				return canonical, err
			}
			ports.homeRoot = func(string) (string, error) { return "", injected }
		}},
	} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(tc.name, func(t *testing.T) {
			fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-ports-"+strings.ReplaceAll(tc.name, " ", "-"))
			options := checkpointBoundaryOptions(fixture, worktree, source)
			ports := productionSessionCheckpointPorts()
			var held *os.File
			tc.set(&ports, &held)
			_, err := createSessionCheckpointWithPorts(context.Background(), options, ports)
			if !errors.Is(err, injected) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkpoint boundary error = %v, want %q and injected cause", err, tc.want)
			}
			if held != nil {
				if _, statErr := held.Stat(); !errors.Is(statErr, os.ErrClosed) {
					t.Fatalf("canonical descriptor after failure = %v, want closed", statErr)
				}
			}
			if _, statErr := os.Stat(filepath.Join(fixture.home, sessionmove.DirName, options.HandoffID)); !os.IsNotExist(statErr) {
				t.Fatalf("pre-admission fault left a handoff: %v", statErr)
			}
		})
	}
}

//nolint:paralleltest // newSessionCheckpointFixture configures process-wide Git and agent environment
func TestE2ESessionCheckpointRejectsOversizeRequestBeforePush(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-oversize-request")
	options := checkpointBoundaryOptions(fixture, worktree, source)
	options.Handover.Body = bytes.Repeat([]byte("x"), sessionmove.MaxHandoverContentBytes+1)
	_, err := CreateSessionCheckpoint(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "validate session checkpoint before mutation") {
		t.Fatalf("oversized request = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.home, sessionmove.DirName, options.HandoffID)); !os.IsNotExist(statErr) {
		t.Fatalf("oversized request was admitted: %v", statErr)
	}
}

//nolint:paralleltest // each native fixture configures process-wide Git and agent environment
func TestE2ESessionCheckpointPublicationAndRemoteVerificationFaults(t *testing.T) {
	injected := errors.New("remote verification failed")
	for _, tc := range []struct {
		name, want string
		configure  func(*testing.T, *gitFixture, string, *SessionCheckpointOptions) context.Context
	}{
		{"push fails after authentication", "push exact source commit without force", func(t *testing.T, fixture *gitFixture, _ string, options *SessionCheckpointOptions) context.Context {
			options.afterPushRemoteAuthentication = func() {
				if err := os.Rename(fixture.remote, fixture.remote+"-unavailable"); err != nil {
					t.Fatal(err)
				}
			}
			return context.Background()
		}},
		{"remote tip query fails", "verify exact remote branch tip after push", func(_ *testing.T, _ *gitFixture, _ string, _ *SessionCheckpointOptions) context.Context {
			return withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
				if len(args) > 0 && args[0] == "ls-remote" {
					return nil, injected
				}
				return run()
			})
		}},
		{"remote tip differs", "want exact source commit", func(_ *testing.T, _ *gitFixture, _ string, _ *SessionCheckpointOptions) context.Context {
			return withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
				if len(args) > 0 && args[0] == "ls-remote" {
					return []byte(strings.Repeat("0", 40) + "\trefs/heads/" + strings.TrimPrefix(args[len(args)-1], "refs/heads/") + "\n"), nil
				}
				return run()
			})
		}},
	} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(tc.name, func(t *testing.T) {
			fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-remote-"+strings.ReplaceAll(tc.name, " ", "-"))
			options := checkpointBoundaryOptions(fixture, worktree, source)
			ctx := tc.configure(t, fixture, worktree, &options)
			result, err := CreateSessionCheckpoint(ctx, options)
			if err == nil || !strings.Contains(err.Error(), tc.want) || result.Request.HandoffID != "" {
				t.Fatalf("remote boundary = (%#v, %v), want %q and no admission", result, err, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(fixture.home, sessionmove.DirName, options.HandoffID)); !os.IsNotExist(statErr) {
				t.Fatalf("remote failure admitted handoff: %v", statErr)
			}
		})
	}
}

//nolint:paralleltest // each native fixture configures process-wide Git and agent environment
func TestE2ESessionCheckpointAdmissionAndRepairFaults(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		prepare    func(*testing.T, *gitFixture, string, *SessionCheckpointOptions, context.CancelFunc)
	}{
		{"store path is a regular file", "persist exact session move request", func(t *testing.T, fixture *gitFixture, _ string, _ *SessionCheckpointOptions, _ context.CancelFunc) {
			if err := os.WriteFile(filepath.Join(fixture.home, sessionmove.DirName), []byte("occupied"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"offer projection disappears after admission", "record exact source Work Log offer evidence", func(t *testing.T, _ *gitFixture, worktree string, options *SessionCheckpointOptions, _ context.CancelFunc) {
			options.afterAdmission = func() error {
				projection := filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName)
				if err := os.Rename(projection, projection+"-hidden"); err != nil {
					t.Fatal(err)
				}
				return nil
			}
		}},
	} {
		//nolint:paralleltest // each fixture calls t.Setenv and runs native Git
		t.Run(tc.name, func(t *testing.T) {
			fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-private-"+strings.ReplaceAll(tc.name, " ", "-"))
			options := checkpointBoundaryOptions(fixture, worktree, source)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tc.prepare(t, fixture, worktree, &options, cancel)
			result, err := CreateSessionCheckpoint(ctx, options)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("private boundary = (%#v, %v), want %q", result, err, tc.want)
			}
			if tc.name == "offer projection disappears after admission" &&
				(result.Request.HandoffID != options.HandoffID || result.Digest != sessionmove.DigestBytes(result.RequestBytes)) {
				t.Fatalf("post-admission failure lost immutable request authority: %#v", result)
			}
		})
	}
}

//nolint:paralleltest // newSessionCheckpointFixture configures process-wide Git and agent environment
func TestE2ESessionCheckpointReplayRefusesExistingAdmittedRequest(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-replay")
	options := checkpointBoundaryOptions(fixture, worktree, source)
	first, err := CreateSessionCheckpoint(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateSessionCheckpoint(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "unexpectedly replayed") || second.Request.HandoffID != "" {
		t.Fatalf("repeat checkpoint = (%#v, %v), want immutable replay refusal", second, err)
	}
	stored, err := sessionmove.NewStore(filepath.Join(fixture.home, sessionmove.DirName)).Load(first.Request.HandoffID)
	if err != nil || stored.Digest != first.Digest {
		t.Fatalf("replay changed stored request = (%#v, %v)", stored, err)
	}
}

//nolint:paralleltest // newSessionCheckpointFixture configures process-wide Git and agent environment
func TestE2ESessionCheckpointReauthenticatesBeforeFirstPush(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-prepush-remote-change")
	options := checkpointBoundaryOptions(fixture, worktree, source)
	pushReads := 0
	ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
		output, err := run()
		if err == nil && len(args) > 2 && args[0] == "remote" && args[1] == "get-url" && strings.Contains(strings.Join(args, " "), "--push") {
			pushReads++
			if pushReads == 3 {
				gitTest(t, worktree, "remote", "set-url", "--push", "origin", "https://other.invalid/acme/app.git")
			}
		}
		return output, err
	})
	result, err := CreateSessionCheckpoint(ctx, options)
	if pushReads < 4 || err == nil || !strings.Contains(err.Error(), "origin remote changed after checkpoint validation") || result.Request.HandoffID != "" {
		t.Fatalf("prepush reauthentication = (%#v, %v), push reads %d", result, err, pushReads)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.home, sessionmove.DirName, options.HandoffID)); !os.IsNotExist(statErr) {
		t.Fatalf("changed remote was admitted: %v", statErr)
	}
}

//nolint:paralleltest // newSessionCheckpointFixture configures process-wide Git and agent environment
func TestE2ESessionCheckpointRetainsAdmittedIdentityWhenExecutionLockIsHeld(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-held-execution-lock")
	options := checkpointBoundaryOptions(fixture, worktree, source)
	store := sessionmove.NewStore(filepath.Join(fixture.home, sessionmove.DirName))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var held *sessionmove.ExecutionLock
	options.afterAdmission = func() error {
		state, err := store.Load(options.HandoffID)
		if err != nil {
			return err
		}
		held, err = store.AcquireExecutionLock(context.Background(), options.HandoffID, state.Digest)
		if err != nil {
			return err
		}
		cancel()
		return nil
	}
	result, err := CreateSessionCheckpoint(ctx, options)
	if held != nil {
		defer func() { _ = held.Close() }()
	}
	if err == nil || !strings.Contains(err.Error(), "retain exact source checkpoint aggregate") || !errors.Is(err, context.Canceled) {
		t.Fatalf("held execution lock error = %v", err)
	}
	if result.Request.HandoffID != options.HandoffID || result.Digest != sessionmove.DigestBytes(result.RequestBytes) || result.WorkLogEvent.ID != "" {
		t.Fatalf("lock failure lost exact admitted identity: %#v", result)
	}
}
