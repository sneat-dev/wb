package worktrees

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type changingReceiveFence struct{ checks, validChecks int }

func (f *changingReceiveFence) HeldForSession(string, string, string) bool {
	f.checks++
	return f.checks <= f.validChecks
}

func (f *changingReceiveFence) RetainSessionDir(string, string, string) (*os.File, error) {
	return nil, errors.New("unused by operation-lock test")
}

var _ sessionauthority.Fence = (*changingReceiveFence)(nil)

func TestSupersessionReceiptRejectsUnreadableMalformedAndTrailingData(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, tc := range []struct {
		name, content, want string
		missing             bool
	}{
		{name: "missing", missing: true, want: "read supersession receipt"},
		{name: "malformed", content: "{", want: "decode supersession receipt"},
		{name: "trailing", content: "{} {}", want: "contains trailing JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(root, tc.name+".json")
			if !tc.missing {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			receipt, rejection := supersessionReceiptForEntry(context.Background(), path, ListResult{})
			if receipt != nil || !strings.Contains(rejection, tc.want) {
				t.Fatalf("receipt = %#v, rejection = %q; want %q", receipt, rejection, tc.want)
			}
		})
	}
}

func TestLifecycleReportRejectsUnencodablePayloadBeforePublication(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path, err := writeLifecycleReportInjected(root, "cleanup", make(chan int), nil)
	if path != "" || err == nil || !strings.Contains(err.Error(), "encode cleanup report") {
		t.Fatalf("report path = %q, error = %v", path, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "cleanup.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid report was published: %v", err)
	}
}

func TestCleanupWorktreeRejectsMissingOwnerAndAbsoluteParent(t *testing.T) {
	t.Parallel()
	task := newHostLevelCleanupTaskFixture(t)
	path := filepath.Join(task.taskPath, "missing-owner", "app")
	if _, err := openCleanupWorktree(task, CleanupResult{ListResult: ListResult{WorktreeDir: path}}); err == nil || !strings.Contains(err.Error(), "open cleanup worktree parent") {
		t.Fatalf("missing owner error = %v", err)
	}
	path = filepath.Join(t.TempDir(), "missing-parent", "app")
	if _, err := openAbsoluteCleanupWorktree(task, path, "adopted", "repository"); err == nil || !strings.Contains(err.Error(), "open adopted worktree parent") {
		t.Fatalf("missing absolute parent error = %v", err)
	}
}

func TestPreparedTargetClaimReportsUnreadableExistingRecord(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "claims"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "claims", "claim.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runDir.Close() })
	replayed, err := publishPreparedTargetClaim(runDir, workLogClaim{ClaimID: "claim"}, "conflict", "publish claim")
	if replayed || err == nil || !strings.Contains(err.Error(), "unexpected end of JSON input") {
		t.Fatalf("unreadable claim = replayed %t, error %v", replayed, err)
	}
}

//nolint:paralleltest // Subtests replace the package-level immutable-write seam.
func TestPreparedTargetClaimPreservesWriteFailureContext(t *testing.T) {
	for _, tc := range []struct{ name, context string }{
		{"contextual", "publish immutable target claim"},
		{"plain", ""},
	} {
		//nolint:paralleltest // Each case replaces the package-level immutable-write seam.
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			claims := filepath.Join(root, "claims")
			if err := os.Mkdir(claims, 0o700); err != nil {
				t.Fatal(err)
			}
			beforeRename := writeBytesImmutableAtBeforeRename
			writeBytesImmutableAtBeforeRename = func(_ *os.File, name string) {
				if name == "claim.json" {
					if err := os.WriteFile(filepath.Join(claims, name), []byte("competing claim\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			t.Cleanup(func() { writeBytesImmutableAtBeforeRename = beforeRename })
			runDir, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runDir.Close() })
			replayed, err := publishPreparedTargetClaim(runDir, workLogClaim{ClaimID: "claim"}, "conflict", tc.context)
			if replayed || err == nil || (tc.context != "" && !strings.Contains(err.Error(), tc.context)) {
				t.Fatalf("claim write = replayed %t, error %v", replayed, err)
			}
		})
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git environment.
func TestBranchClassificationKeepsRejectedSupersessionEvidence(t *testing.T) {
	fixture := newGitFixture(t)
	head, err := gitCanonical(context.Background(), mustOpenCanonical(t, fixture.canonical), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	repository := discover.Repo{Org: "acme", Name: "app", Path: fixture.canonical}
	entry := classifyBranch(context.Background(), repository,
		branchSweepOptions{Base: "main", SupersededBy: filepath.Join(t.TempDir(), "missing.json")},
		branchRef{Name: "feature/rejected-receipt", SHA: head}, "local", head, "main", nil, nil, nil)
	if entry.Disposition != BranchContained || !strings.Contains(entry.SupersessionRejection, "read supersession receipt") {
		t.Fatalf("classified entry = %#v", entry)
	}
}

func TestDeletionRecheckRejectsSemanticallyInvalidSupersessionReceipt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := supersessionFileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	result := BranchCleanupResult{BranchEntry: BranchEntry{
		Repository: "acme/app", Branch: "feature", Base: "main", SupersededAtOrigin: true,
		SupersessionReceipt: path, SupersessionSHA256: digest,
	}}
	if recheckDeletionEvidence(context.Background(), t.TempDir(), retirementHead, retirementTarget, &result) ||
		result.Outcome != "failed" || result.Error == "" || strings.Contains(result.Error, "bytes changed") {
		t.Fatalf("invalid receipt recheck = %#v", result)
	}
}

//nolint:paralleltest // t.Setenv and os.Stderr redirection affect the process.
func TestSecureGitHelperRejectsUnavailableWriteRootDescriptor(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", root)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStderr := os.Stderr
	os.Stderr = writer
	status := runSecureGitHelper("test secure Git", root,
		[]gitFilesystemCapabilityRoot{{path: filepath.Join(root, "unheld")}}, "git", []string{"status"}, nil)
	os.Stderr = originalStderr
	_ = writer.Close()
	output, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if status != 1 {
		t.Fatalf("invalid capability status = %d, want 1", status)
	}
	if !strings.Contains(string(output), "git capability root descriptor is unavailable") {
		t.Fatalf("invalid capability diagnostic = %q", output)
	}
}

//nolint:paralleltest // t.Setenv changes process-wide configuration.
func TestRetiredArchiveDefaultInspectorFailsClosedOnCanceledRead(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan, err := PlanRetiredArchivePreflight(ctx, "acme/app", nil)
	if err != nil || plan.Outcome != "refused" || plan.Refusal == "" {
		t.Fatalf("default inspector plan = %#v, error = %v", plan, err)
	}
}

func TestQuarantineNameExhaustionPreservesCheckout(t *testing.T) {
	t.Parallel()
	root, parent, checkout := newQuarantineCheckout(t)
	const retired = ".wb-retired-checkout-collision"
	attempts := 0
	if err := os.Mkdir(filepath.Join(root, retired), 0o700); err != nil {
		t.Fatal(err)
	}
	token := func(size int) string {
		if size != 16 {
			t.Fatalf("token size = %d", size)
		}
		return "collision"
	}
	move := func(from *os.File, fromName string, to *os.File, toName string, expected *os.File, _ func()) (*os.File, error) {
		attempts++
		if from != parent || to != parent || expected != checkout || fromName != "checkout" || toName != retired {
			t.Fatalf("unexpected quarantine move: %q to %q", fromName, toName)
		}
		return nil, syscall.EEXIST
	}
	if err := quarantineSecureStageCheckoutWith(parent, checkout, token, move); err == nil || !strings.Contains(err.Error(), "collision-free staged checkout") {
		t.Fatalf("exhausted quarantine error = %v", err)
	}
	if attempts != 16 {
		t.Fatalf("collision attempts = %d, want 16", attempts)
	}
	if _, err := os.Stat(filepath.Join(root, "checkout")); err != nil {
		t.Fatalf("checkout changed after name collisions: %v", err)
	}
}

func TestQuarantineMoveClosesReturnedHandleOnFailure(t *testing.T) {
	t.Parallel()
	_, parent, checkout := newQuarantineCheckout(t)
	returned, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	moved, name, err := quarantineDirectoryEntryNamedWith(parent, "checkout", checkout, ".retired-",
		func(int) string { return "candidate" },
		func(*os.File, string, *os.File, string, *os.File, func()) (*os.File, error) {
			return returned, errors.New("failed after retaining destination")
		})
	if moved != nil || name != "" || err == nil || !strings.Contains(err.Error(), "failed after retaining destination") {
		t.Fatalf("failed move = %v, %q, %v", moved, name, err)
	}
	if _, err := returned.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("failed move handle was not closed: %v", err)
	}
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide Git environment.
func TestInterruptedReceiveReuseRefusesAmbiguousStagesAndLostRoot(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"ambiguous stages", "multiple active receive stages"},
		{"lost operation root", "open completed interrupted receive root"},
	} {
		//nolint:paralleltest // The fixture sets process-wide Git environment for this case.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			created, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
				ProjectsRoot: fixture.projectsRoot, Request: fixture.request,
			})
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(created.WorktreeDir)
			state := &sessionReceiveState{ctx: context.Background(), projectsRoot: fixture.projectsRoot,
				canonical: mustOpenCanonical(t, fixture.canonical), canonicalPath: fixture.canonical,
				repository: "acme/app", spec: sessionReceiveStateSpec(fixture),
				lock: operationLock{interrupted: true}}
			if tc.name == "ambiguous stages" {
				for _, suffix := range []string{"0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"} {
					if err := os.Mkdir(filepath.Join(root, ".wb-stage-"+suffix), 0o700); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				lists := 0
				state.ctx = withCanonicalGitInterceptor(state.ctx, func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
					result, err := run()
					if len(args) >= 2 && args[0] == "worktree" && args[1] == "list" {
						lists++
						if lists == 3 {
							if renameErr := os.Rename(root, root+"-moved"); renameErr != nil {
								t.Fatal(renameErr)
							}
						}
					}
					return result, err
				})
			}
			_, reused, err := state.reuseRegistered()
			if reused || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("interrupted reuse = %t, error %v", reused, err)
			}
		})
	}
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide Git environment.
func TestInterruptedReceiveReuseReportsRootCloseFailure(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	if _, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
		ProjectsRoot: fixture.projectsRoot, Request: fixture.request,
	}); err != nil {
		t.Fatal(err)
	}
	state := &sessionReceiveState{ctx: context.Background(), projectsRoot: fixture.projectsRoot,
		canonical: mustOpenCanonical(t, fixture.canonical), canonicalPath: fixture.canonical,
		repository: "acme/app", spec: sessionReceiveStateSpec(fixture),
		lock: operationLock{interrupted: true},
		closeInterruptedRoot: func(root *os.File) error {
			_ = root.Close()
			return errors.New("injected root close failure")
		}}
	_, reused, err := state.reuseRegistered()
	if reused || err == nil || !strings.Contains(err.Error(), "close completed interrupted receive root: injected root close failure") {
		t.Fatalf("interrupted close = reused %t, error %v", reused, err)
	}
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide Git environment.
func TestSessionReceiveSourceReportsExactFetchAndTipFailures(t *testing.T) {
	for _, tc := range []struct {
		name, operation, want string
	}{
		{name: "fetch", operation: "fetch", want: "fetch live session branch"},
		{name: "resolve exact tip", operation: "rev-parse", want: "resolve live fetched branch tip"},
	} {
		//nolint:paralleltest // The fixture sets process-wide Git environment for this case.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			state := &sessionReceiveState{}
			t.Cleanup(state.close)
			seen := false
			ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
				if !seen && len(args) > 0 && args[0] == tc.operation &&
					(tc.operation != "rev-parse" || (len(args) == 3 && args[2] == sessionReceiveFetchRef(fixture.request.HandoffID)+"^{commit}")) {
					seen = true
					return nil, errors.New("injected exact source Git failure")
				}
				return run()
			})
			err := state.prepareSource(ctx, SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: sessionReceiveStateSpec(fixture)}, nil)
			if !seen || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("source preparation error = %v; injected = %t", err, seen)
			}
		})
	}
}

func TestSessionReceiveOperationRechecksInterruptedAuthorityAndMetadata(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, metadata, want string
		validChecks          int
	}{
		{name: "authority changed", metadata: "operation=session-op\npid=2147483647\n", validChecks: 1, want: "authority changed"},
		{name: "invalid lock metadata", metadata: "operation=session-op\npid=invalid\n", validChecks: 2, want: "validate interrupted target receive lock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			projects := t.TempDir()
			home, err := wbhome.Root(projects)
			if err != nil {
				t.Fatal(err)
			}
			operation := filepath.Join(home, "worktrees", "session-op")
			if err := os.MkdirAll(operation, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(operation, ".lock"), []byte(tc.metadata), 0o600); err != nil {
				t.Fatal(err)
			}
			fence := &changingReceiveFence{validChecks: tc.validChecks}
			state := &sessionReceiveState{ctx: context.Background(), projectsRoot: projects,
				spec: SessionReceiveSpec{OperationID: "op", Fence: fence}}
			t.Cleanup(state.close)
			if err := state.prepareOperation(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("interrupted operation error = %v", err)
			}
			if state.lock.file != nil {
				t.Fatal("interrupted lock descriptor remained held after refusal")
			}
		})
	}
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide Git environment.
func TestSessionReceiveRegisteredPinReportsInspectionAndPathErrors(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Git inspection", "injected pin inspection failure"},
		{"outside deterministic path", "outside its deterministic repository path"},
	} {
		//nolint:paralleltest // The fixture sets process-wide Git environment for this case.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			spec := sessionReceiveStateSpec(fixture)
			canonical := mustOpenCanonical(t, fixture.canonical)
			state := &sessionReceiveState{ctx: context.Background(), projectsRoot: fixture.projectsRoot,
				canonical: canonical, canonicalPath: fixture.canonical, repository: "acme/app", spec: spec}
			if tc.name == "Git inspection" {
				state.ctx = withCanonicalGitInterceptor(state.ctx, func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
					if len(args) >= 2 && args[0] == "worktree" && args[1] == "list" {
						return nil, errors.New("injected pin inspection failure")
					}
					return run()
				})
			} else {
				outside := filepath.Join(fixture.root, "outside", "wrong-name")
				if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
					t.Fatal(err)
				}
				gitTest(t, fixture.canonical, "worktree", "add", "--quiet", "-b", spec.PinBranch, outside, spec.Commit)
			}
			_, reused, err := state.reuseRegistered()
			if reused || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("registered reuse = %t, error %v", reused, err)
			}
		})
	}
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide Git environment.
func TestSessionReceivePlacementRejectsChangedLayoutAndPinInspection(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"invalid placement policy", "worktrees config"},
		{"symlinked local root", "create canonical .worktrees root"},
		{"occupied shared root", "occupied-shared-root"},
		{"non-directory target", "worktree destination is not a directory"},
		{"pin registration query", "injected pin registration failure"},
	} {
		//nolint:paralleltest // The fixture sets process-wide Git environment for this case.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			state := &sessionReceiveState{}
			t.Cleanup(state.close)
			if err := state.prepareSource(context.Background(), SessionMemberReceiveOptions{
				ProjectsRoot: fixture.projectsRoot, Spec: sessionReceiveStateSpec(fixture),
			}, nil); err != nil {
				t.Fatal(err)
			}
			if err := state.prepareOperation(); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(fixture.root, "config", "wb", "worktrees.yaml")
			switch tc.name {
			case "invalid placement policy":
				if err := os.WriteFile(config, []byte("version: [not-an-integer]\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlinked local root":
				if err := os.Symlink(t.TempDir(), filepath.Join(fixture.canonical, ".worktrees")); err != nil {
					t.Fatal(err)
				}
			case "occupied shared root":
				shared := filepath.Join(fixture.root, "occupied-shared-root")
				if err := os.WriteFile(shared, []byte("occupied"), 0o600); err != nil {
					t.Fatal(err)
				}
				mustWriteBranchConfig(t, config, "version: 1\nworktrees:\n  root: "+shared+"\n")
			case "non-directory target":
				root := filepath.Join(fixture.canonical, ".worktrees")
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "session-"+fixture.request.HandoffID), []byte("occupied"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "pin registration query":
				if _, err := gitCanonical(context.Background(), state.canonical, "branch", state.spec.PinBranch, state.spec.Commit); err != nil {
					t.Fatal(err)
				}
				resolvedBranch := false
				state.ctx = withCanonicalGitInterceptor(state.ctx, func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
					if len(args) == 3 && args[0] == "rev-parse" && args[2] == "refs/heads/"+state.spec.PinBranch+"^{commit}" {
						resolvedBranch = true
					}
					if resolvedBranch && len(args) >= 2 && args[0] == "worktree" && args[1] == "list" {
						return nil, errors.New("injected pin registration failure")
					}
					return run()
				})
			}
			_, err := state.placeTarget()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("placement %s error = %v", tc.name, err)
			}
		})
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git environment.
func TestPerWorktreeExcludeReportsAtomicWriteFailure(t *testing.T) {
	fixture := newGitFixture(t)
	exclude := filepath.Join(fixture.canonical, ".git", "info", "exclude")
	info := filepath.Dir(exclude)
	if err := os.Chmod(info, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(info, 0o700) })
	if err := ensurePerWorktreeGitExclude(fixture.canonical, []string{"custom-test-rule"}, "update exclude"); err == nil || !strings.Contains(err.Error(), "update exclude") {
		t.Fatalf("atomic exclude write error = %v", err)
	}
}

func TestClaimProjectionCorroborationErrorKeepsSelectionSemantics(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	projection := workLogProjection{Version: 1, EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("a", 64), Lifecycle: "active"}
	if err := writeWorkLogProjectionAt(worktree, projection); err != nil {
		t.Fatal(err)
	}
	if err := writeLegacyProjectionAt(worktree, projection); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkLogProjectionForClaim(t.TempDir(), worktree); err == nil || strings.Contains(err.Error(), "corroborate legacy") {
		t.Fatalf("corroboration of equal projections error = %v", err)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git environment.
func TestLegacyClaimProjectionReportsMigrationWriteFailure(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "legacy-write-failure",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create claim = %v, %v", created, err)
	}
	worktree := created[0].WorktreeDir
	current := filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName)
	content, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, legacyWorkLogProjectionName), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(fixture.canonical, ".git", "info", "exclude")
	if err := os.WriteFile(exclude, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	info := filepath.Dir(exclude)
	if err := os.Chmod(info, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(info, 0o700) })
	if _, err := readWorkLogProjectionForClaim(fixture.home, worktree); err == nil || !strings.Contains(err.Error(), "migrate legacy work-log projection") {
		t.Fatalf("migration write error = %v", err)
	}
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide Git environment.
func TestInterruptedReceiveRecoveryRejectsMissingPublishedAndStagedCheckout(t *testing.T) {
	for _, tc := range []struct {
		name, want  string
		finalExists bool
	}{
		{name: "published missing", finalExists: true, want: "open interrupted published target"},
		{name: "staged missing", want: "open exact interrupted staged checkout"},
	} {
		//nolint:paralleltest // The fixture sets process-wide Git environment for this case.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			canonical := mustOpenCanonical(t, fixture.canonical)
			spec := sessionReceiveStateSpec(fixture)
			_, operationRoot, parent, name, finalPath, err := sessionReceivePhysicalCoordinates(
				context.Background(), fixture.projectsRoot, canonical, spec, "acme/app")
			if err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(operationRoot, ".wb-stage-0123456789abcdef0123456789abcdef")
			if err := os.MkdirAll(stage, 0o700); err != nil {
				t.Fatal(err)
			}
			checkout := filepath.Join(stage, "checkout")
			gitTest(t, fixture.canonical, "worktree", "add", "--quiet", "-b", spec.PinBranch, checkout, spec.Commit)
			if err := os.RemoveAll(checkout); err != nil {
				t.Fatal(err)
			}
			operationDirectory, err := os.Open(operationRoot)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = operationDirectory.Close() })
			_, err = recoverInterruptedSessionReceivePublication(context.Background(), canonical,
				operationRoot, operationDirectory, parent, name, finalPath, spec.PinBranch, spec.Commit, tc.finalExists)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("recovery %s error = %v", tc.name, err)
			}
		})
	}
}

//nolint:paralleltest // newRepositoryTransferFixture sets process-wide Git environment.
func TestRepositoryTransferRejectsChangedMovedGitAdministration(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "transfer-common-drift", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create transfer claim = %v, %v", created, err)
	}
	fixture.moveRemote(t)
	changed := false
	options := RepositoryRelocateOptions{
		ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote,
		DefaultBranch: "main", Apply: true,
		beforeWorkLogCompletion: func(worktree string) error {
			admin := linkedWorktreeAdmin(t, worktree)
			other := filepath.Join(t.TempDir(), "other.git")
			if err := os.Mkdir(other, 0o700); err != nil {
				return err
			}
			for _, name := range []string{"objects", "refs"} {
				if err := os.Symlink(filepath.Join(fixture.destination, ".git", name), filepath.Join(other, name)); err != nil {
					return err
				}
			}
			config, err := os.ReadFile(filepath.Join(fixture.destination, ".git", "config"))
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(other, "config"), config, 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(admin, "commondir"), []byte(other+"\n"), 0o600); err != nil {
				return err
			}
			changed = true
			return nil
		},
	}
	_, err = RelocateRepository(context.Background(), options)
	if !changed || err == nil || !strings.Contains(err.Error(), "verify relocated Git administration") {
		t.Fatalf("Git administration drift error = %v; changed = %t", err, changed)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git environment.
func TestCreateRollsBackWhenPostCheckoutHookRemovesStagedCheckout(t *testing.T) {
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	hook := filepath.Join(fixture.canonical, ".git", "hooks", "post-checkout")
	contents := "#!/bin/sh\ncase \"$PWD\" in */.wb-stage-*/checkout) rm -rf \"$PWD\" ;; esac\nexit 0\n"
	if err := testenv.WriteExecutableFile(hook, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "removed-staged-checkout",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err == nil || !strings.Contains(err.Error(), "open staged worktree checkout") {
		t.Fatalf("removed staged checkout error = %v", err)
	}
	assertFailedCreateRolledBack(t, fixture, "removed-staged-checkout")
}

func preparedReceivePlacementState(t *testing.T, fixture *sessionReceiveFixture) *sessionReceiveState {
	t.Helper()
	state := &sessionReceiveState{}
	t.Cleanup(state.close)
	if err := state.prepareSource(context.Background(), SessionMemberReceiveOptions{
		ProjectsRoot: fixture.projectsRoot, Spec: sessionReceiveStateSpec(fixture),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := state.prepareOperation(); err != nil {
		t.Fatal(err)
	}
	return state
}

func linkedWorktreeAdmin(t *testing.T, worktree string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	path, ok := strings.CutPrefix(strings.TrimSpace(string(content)), "gitdir: ")
	if !ok || !filepath.IsAbs(path) {
		t.Fatalf("invalid linked-worktree .git pointer: %q", content)
	}
	return path
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide Git environment.
func TestSessionReceivePlacementHandlesExistingTargetAndPinFailures(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"existing target", ""},
		{"inspect pin branch", "injected branch inspection failure"},
		{"wrong pin tip", "does not identify exact admitted commit"},
		{"occupied pin branch", "already checked out at conflicting path"},
		{"staged add failure", "create pinned target worktree"},
		{"post-publication drift", "verify new pinned target worktree"},
	} {
		//nolint:paralleltest // The fixture sets process-wide Git environment for this case.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			if tc.name == "existing target" {
				if _, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
					ProjectsRoot: fixture.projectsRoot, Request: fixture.request,
				}); err != nil {
					t.Fatal(err)
				}
			}
			state := preparedReceivePlacementState(t, fixture)
			switch tc.name {
			case "inspect pin branch":
				state.ctx = withCanonicalGitInterceptor(state.ctx, func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
					if len(args) >= 1 && args[0] == "branch" {
						return nil, errors.New("injected branch inspection failure")
					}
					return run()
				})
			case "wrong pin tip":
				if _, err := gitCanonical(context.Background(), state.canonical, "branch", state.spec.PinBranch, state.spec.SourceWorkCommit); err != nil {
					t.Fatal(err)
				}
			case "occupied pin branch":
				outside := filepath.Join(fixture.root, "outside", "pin")
				if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
					t.Fatal(err)
				}
				gitTest(t, fixture.canonical, "worktree", "add", "--quiet", "-b", state.spec.PinBranch, outside, state.spec.Commit)
			case "staged add failure":
				hook := filepath.Join(fixture.canonical, ".git", "hooks", "post-checkout")
				if err := testenv.WriteExecutableFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "post-publication drift":
				state.afterTargetPublication = func(path string) {
					if err := os.WriteFile(filepath.Join(path, "untracked-drift"), []byte("changed"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := state.placeTarget()
			if tc.name == "existing target" {
				if err != nil || !result.Reused || !sameSessionReceivePath(result.WorktreeDir, fixture.targetWorktree()) {
					t.Fatalf("existing target = %#v, error %v", result, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("placement %s error = %v", tc.name, err)
			}
			if tc.name == "post-publication drift" {
				path := fixture.targetWorktree()
				if state.publication == nil {
					t.Fatal("published checkout was not retained for recovery")
				}
				if _, statErr := os.Stat(filepath.Join(path, "untracked-drift")); statErr != nil {
					t.Fatalf("published drift was lost: %v", statErr)
				}
				occupied, registered, inspectErr := branchWorktreeCanonical(context.Background(), state.canonical, state.spec.PinBranch)
				if inspectErr != nil || !occupied || !sameSessionReceivePath(registered, path) {
					t.Fatalf("pin after verification failure = occupied %t, path %q, error %v", occupied, registered, inspectErr)
				}
			}
		})
	}
}
