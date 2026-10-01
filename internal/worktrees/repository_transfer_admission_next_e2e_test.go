//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

//nolint:paralleltest // the existing native fixture sets process-wide WB and Git environment.
func TestE2ERepositoryTransferAdmissionInventoryNativeRefusals(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	source := fixture.canonical
	// Public PlanCloneMove accepts caller spellings; Git reports the canonical
	// registry path. Preserve refusal rather than silently treating a different
	// spelling as the required exact canonical registration.
	entries, err := repositoryRelocateWorktrees(context.Background(), source+string(os.PathSeparator)+".", fixture.destination)
	if err == nil || entries != nil || !strings.Contains(err.Error(), "canonical repository is absent") {
		t.Fatalf("nonexact registry=%+v %v", entries, err)
	}
	var cause error
	called := false
	entries, err = repositoryRelocateWorktreesWithReads(context.Background(), source, fixture.destination, func(base, target string) (string, error) {
		called = true
		if base != source || target != source {
			t.Fatalf("relative mapping inputs=%q %q", base, target)
		}
		// Native filepath.Rel rejects mixed absolute/relative path input. This
		// caller-local mapper contract preserves its genuine error, without cwd
		// mutation or a claim that valid Git registration emitted this input.
		value, err := filepath.Rel(base, filepath.Base(target))
		cause = err
		return value, err
	}, gitRawOutput)
	if !called || cause == nil || !errors.Is(err, cause) || entries != nil {
		t.Fatalf("native mapping cause=%v entries=%+v err=%v called=%v", cause, entries, err, called)
	}
	if got := gitTestOutput(t, source, "rev-parse", "HEAD"); got == "" {
		t.Fatal("inventory refusal lost source HEAD")
	}
}

//nolint:paralleltest // the existing native fixture sets process-wide WB and Git environment.
func TestE2ERepositoryTransferAdmissionCleanupJSONFailureRestoresDestination(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	fixture.moveRemote(t)
	fixture.cloneDestination(t)
	sourceHead := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	destinationHead := gitTestOutput(t, fixture.destination, "rev-parse", "HEAD")
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true, Now: func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }}
	result, err := RelocateRepository(context.Background(), options)
	var marshalError *json.MarshalerError
	if !errors.As(err, &marshalError) || result.Applied || len(result.ReceiptPaths) != 0 {
		t.Fatalf("cleanup publication=%+v %v", result, err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != sourceHead {
		t.Fatalf("source changed=%s", got)
	}
	if got := gitTestOutput(t, fixture.destination, "rev-parse", "HEAD"); got != destinationHead {
		t.Fatalf("disposable destination not restored=%s", got)
	}
	if _, err := os.Lstat(result.RetiredDestinationDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed publication left quarantine: %v", err)
	}
}

//nolint:paralleltest // Create and the existing native fixture set process-wide WB and Git environment.
func TestE2ERepositoryTransferAdmissionFinalizationRetainsTimestampFailure(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "transfer-admission-finalize", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	claim, _, _, err := activeWorkLogClaim(fixture.home, created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	fixture.moveRemote(t)
	pause := errors.New("stop before transfer completion")
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true, beforeWorkLogCompletion: func(string) error { return pause }}
	result, err := RelocateRepository(context.Background(), options)
	if !errors.Is(err, pause) || result.Applied {
		t.Fatalf("interrupted transfer=%+v %v", result, err)
	}
	moved := filepath.Join(fixture.destination, ".worktrees", "transfer-admission-finalize")
	head := gitTestOutput(t, moved, "rev-parse", "HEAD")
	intent, intentPath, err := pendingRelocationIntent(fixture.home, claim, moved, claim.Branch, head)
	if err != nil || intent == nil {
		t.Fatalf("bound pending fixture=%+v %v", intent, err)
	}
	if err := corroborateRepositoryRelocation(context.Background(), moved, options.DestinationRepository); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	options.beforeWorkLogCompletion = nil
	projectionPath := filepath.Join(moved, workLogProjectionDirectory, workLogProjectionName)
	projection, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectionPath, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if receipts, err := FinalizeRepositoryTransferWorkLogs(context.Background(), options); err == nil || receipts != nil || !strings.Contains(err.Error(), "resolve transferred Work Log claim") {
		t.Fatalf("ambiguous projection completed transfer: %v %v", receipts, err)
	}
	if err := os.WriteFile(projectionPath, projection, 0600); err != nil {
		t.Fatal(err)
	}
	options.Now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
	receipts, err := FinalizeRepositoryTransferWorkLogs(context.Background(), options)
	var marshalError *json.MarshalerError
	if !errors.As(err, &marshalError) || receipts != nil {
		t.Fatalf("completion error replaced=%v %v", receipts, err)
	}
	if raw, err := os.ReadFile(intentPath); err != nil || string(raw) != string(before) {
		t.Fatalf("pending intent changed=%q %v", raw, err)
	}
	receiptPath := filepath.Join(filepath.Dir(intentPath), relocationReceiptName(claim.ClaimID, intent.OperationID))
	if _, err := os.Lstat(receiptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid timestamp published receipt: %v", err)
	}
	if got := gitTestOutput(t, moved, "rev-parse", "HEAD"); got != head {
		t.Fatalf("finalization failure changed checkout=%s", got)
	}
}

//nolint:paralleltest // each existing native fixture sets process-wide WB and Git environment.
func TestE2ERepositoryTransferAdmissionNativePlanningRechecks(t *testing.T) {
	for _, kind := range []string{"registry failure", "destination appeared", "disposable safety changed"} {
		//nolint:paralleltest // each case creates the existing process-environment native Git fixture.
		t.Run(kind, func(t *testing.T) {
			fixture := newRepositoryTransferFixture(t)
			fixture.moveRemote(t)
			if kind == "disposable safety changed" {
				fixture.cloneDestination(t)
			}
			sourceHead := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
			hit := false
			var cause error
			output := func(ctx context.Context, dir string, args ...string) (string, error) {
				if !hit && kind == "registry failure" && dir == fixture.canonical && strings.Join(args, " ") == "worktree list --porcelain" {
					hit = true
					value, err := gitRawOutput(ctx, filepath.Join(t.TempDir(), "missing"), args...)
					cause = err
					return value, err
				}
				value, err := gitRawOutput(ctx, dir, args...)
				if err != nil {
					return value, err
				}
				if !hit && kind == "destination appeared" && dir == fixture.canonical && len(args) > 1 && args[0] == "ls-remote" && args[1] == "--symref" {
					hit = true
					if err := os.MkdirAll(fixture.destination, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(fixture.destination, "occupant"), []byte("retained"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if !hit && kind == "disposable safety changed" && dir == fixture.destination && len(args) > 0 && args[0] == "for-each-ref" {
					hit = true
					// The first native refs snapshot was safe. Drift origin only after that
					// snapshot; the second pre-mutation safety check must observe it.
					gitTest(t, fixture.destination, "remote", "set-url", "origin", fixture.oldRemote)
				}
				return value, nil
			}
			options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
			result, err := relocateRepositoryWithReads(context.Background(), options, CanonicalRepositoryPath, output)
			want := "repository destination changed after planning"
			if kind == "disposable safety changed" {
				want = "repository destination safety changed after planning"
			}
			if !hit || err == nil || result.Applied || len(result.ReceiptPaths) != 0 {
				t.Fatalf("planning refusal=%+v %v hit=%v", result, err, hit)
			}
			if kind == "registry failure" {
				if cause == nil || !errors.Is(err, cause) {
					t.Fatalf("native registry cause=%v err=%v", cause, err)
				}
			} else if !strings.Contains(err.Error(), want) {
				t.Fatalf("wrong recheck boundary: %v", err)
			}
			if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != sourceHead {
				t.Fatalf("admission moved source=%s", got)
			}
			if kind == "destination appeared" {
				if raw, err := os.ReadFile(filepath.Join(fixture.destination, "occupant")); err != nil || string(raw) != "retained" {
					t.Fatalf("occupant changed=%q %v", raw, err)
				}
			}
		})
	}
}

//nolint:paralleltest // each existing native fixture sets process-wide WB and Git environment.
func TestE2ERepositoryTransferAdmissionDisposableQueryContracts(t *testing.T) {
	for _, kind := range []string{"native status failure", "native refs failure", "defensive remote advertisement", "defensive local ref"} {
		//nolint:paralleltest // the existing native Git fixture sets process-wide environment.
		t.Run(kind, func(t *testing.T) {
			fixture := newRepositoryTransferFixture(t)
			fixture.moveRemote(t)
			fixture.cloneDestination(t)
			head := gitTestOutput(t, fixture.destination, "rev-parse", "HEAD")
			index := filepath.Join(fixture.destination, ".git", "index")
			initial, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(index, initial, 0600); err != nil {
					t.Fatal(err)
				}
			})
			hit := false
			output := func(ctx context.Context, dir string, args ...string) (string, error) {
				if !hit && kind == "native refs failure" && len(args) > 0 && args[0] == "for-each-ref" {
					hit = true
					return gitRawOutput(ctx, filepath.Join(t.TempDir(), "missing"), args...)
				}
				value, err := gitRawOutput(ctx, dir, args...)
				if err != nil {
					return value, err
				}
				if !hit && kind == "native status failure" && strings.Join(args, " ") == "remote get-url --all --push origin" {
					hit = true
					if err := os.WriteFile(index, []byte("invalid native index"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				// These two are defensive arbitrary-query-output parser contracts, not
				// claims that native Git emitted malformed advertisements or local refs.
				if !hit && kind == "defensive remote advertisement" && strings.Join(args, " ") == "ls-remote --refs origin" {
					hit = true
					return "malformed advertisement", nil
				}
				if !hit && kind == "defensive local ref" && len(args) > 0 && args[0] == "for-each-ref" {
					hit = true
					return "malformed local ref", nil
				}
				return value, nil
			}
			options := RepositoryRelocateOptions{DestinationRepository: "newco/renamed", DefaultBranch: "main"}
			reason := disposableDestinationReason(context.Background(), fixture.destination, options, head, output)
			wants := map[string]string{"native status failure": "cannot inspect destination status", "native refs failure": "cannot inspect destination refs", "defensive remote advertisement": "destination remote returned an invalid ref advertisement", "defensive local ref": "destination contains an invalid local ref"}
			if !hit || reason != wants[kind] {
				t.Fatalf("disposable refusal=%q want=%q hit=%v", reason, wants[kind], hit)
			}
			if got := gitTestOutput(t, fixture.destination, "rev-parse", "HEAD"); got != head {
				t.Fatalf("refusal changed destinationHEAD=%s", got)
			}
			if kind == "native status failure" {
				if raw, err := os.ReadFile(index); err != nil || string(raw) != "invalid native index" {
					t.Fatalf("refusal replaced observed evidence=%q %v", raw, err)
				}
			}
		})
	}
}

//nolint:paralleltest // the existing native fixture sets process-wide WB and Git environment.
func TestE2ERepositoryTransferAdmissionQuarantineCollisionRetainsBothRepositories(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	fixture.moveRemote(t)
	fixture.cloneDestination(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	remoteHead := gitTestOutput(t, fixture.newRemote, "rev-parse", "refs/heads/main")
	retired := filepath.Join(filepath.Dir(fixture.destination), ".wb-replaced-"+filepath.Base(fixture.destination)+"-"+remoteHead[:12])
	reads := 0
	output := func(ctx context.Context, dir string, args ...string) (string, error) {
		value, err := gitRawOutput(ctx, dir, args...)
		if err == nil && dir == fixture.destination && len(args) > 0 && args[0] == "for-each-ref" {
			reads++
			if reads == 2 {
				if err := os.Mkdir(retired, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(retired, "occupant"), []byte("retained"), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		return value, err
	}
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
	result, err := relocateRepositoryWithReads(context.Background(), options, CanonicalRepositoryPath, output)
	if reads != 2 || err == nil || !strings.Contains(err.Error(), "temporarily quarantine disposable destination") || !result.Eligible || result.Applied || len(result.ReceiptPaths) != 0 {
		t.Fatalf("collision result=%+v err=%v reads=%d", result, err, reads)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
		t.Fatalf("source changed=%s", got)
	}
	if got := gitTestOutput(t, fixture.destination, "rev-parse", "HEAD"); got != remoteHead {
		t.Fatalf("destination changed=%s", got)
	}
	if raw, err := os.ReadFile(filepath.Join(retired, "occupant")); err != nil || string(raw) != "retained" {
		t.Fatalf("occupant=%q %v", raw, err)
	}
}

//nolint:paralleltest // the existing native fixture sets process-wide WB and Git environment.
func TestE2ERepositoryTransferAdmissionCommonDirectoryRefusalRollsBack(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	fixture.moveRemote(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	fault := &failRepositoryTransferGitOnce{Runner: runner.New(), directory: fixture.destination, command: []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}}
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
	result, err := RelocateRepository(withGitRunner(context.Background(), fault), options)
	if !fault.hit || err == nil || !strings.Contains(err.Error(), "verify relocated Git administration") || result.Applied || len(result.ReceiptPaths) != 0 {
		t.Fatalf("administration result=%+v err=%v hit=%v", result, err, fault.hit)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
		t.Fatalf("source changed=%s", got)
	}
	if got := gitTestOutput(t, fixture.canonical, "remote", "get-url", "origin"); got != fixture.oldRemote {
		t.Fatalf("rollback origin=%s", got)
	}
	if _, err := os.Lstat(fixture.destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback left destination: %v", err)
	}
}

//nolint:paralleltest // the existing native fixture sets process-wide WB and Git environment.
func TestE2ERepositoryTransferAdmissionCorruptHeadCannotBecomeUnborn(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	path := filepath.Join(fixture.canonical, ".git", "HEAD")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Error(err)
		}
	})
	hit := false
	output := func(ctx context.Context, dir string, args ...string) (string, error) {
		value, err := gitRawOutput(ctx, dir, args...)
		if err == nil && strings.Join(args, " ") == "worktree list --porcelain" {
			hit = true
			if err := os.WriteFile(path, []byte("invalid HEAD\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return value, err
	}
	entries, err := repositoryRelocateWorktreesWithReads(context.Background(), fixture.canonical, fixture.destination, filepath.Rel, output)
	if !hit || err == nil || entries != nil {
		t.Fatalf("corrupt inventory=%+v err=%v hit=%v", entries, err, hit)
	}
	for _, args := range [][]string{{"rev-parse", "HEAD"}, {"symbolic-ref", "--quiet", "HEAD"}} {
		if _, err := gitRawOutput(context.Background(), fixture.canonical, args...); err == nil {
			t.Fatalf("corrupt HEAD accepted by %v", args)
		}
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "invalid HEAD\n" {
		t.Fatalf("corruption evidence=%q %v", raw, err)
	}
}

//nolint:paralleltest // Create and the existing native fixture set process-wide WB and Git environment.
func TestE2ERepositoryTransferAdmissionFinalizerNativeRaceWindows(t *testing.T) {
	for _, kind := range []string{"journal changed", "origin changed"} {
		//nolint:paralleltest // each case creates a native process-environment fixture.
		t.Run(kind, func(t *testing.T) {
			fixture := newRepositoryTransferFixture(t)
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "transfer-finalizer-race", WorkLog: WorkLogOptions{Model: "unknown"}})
			if err != nil {
				t.Fatal(err)
			}
			claim, _, _, err := activeWorkLogClaim(fixture.home, created[0].WorktreeDir)
			if err != nil {
				t.Fatal(err)
			}
			fixture.moveRemote(t)
			pause := errors.New("pause before completion")
			options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true, beforeWorkLogCompletion: func(string) error { return pause }}
			result, err := RelocateRepository(context.Background(), options)
			if !errors.Is(err, pause) || result.Applied {
				t.Fatalf("interruption=%+v %v", result, err)
			}
			moved := filepath.Join(fixture.destination, ".worktrees", "transfer-finalizer-race")
			head := gitTestOutput(t, moved, "rev-parse", "HEAD")
			intent, intentPath, err := pendingRelocationIntent(fixture.home, claim, moved, claim.Branch, head)
			if err != nil || intent == nil {
				t.Fatalf("native pending=%+v %v", intent, err)
			}
			before, err := os.ReadFile(intentPath)
			if err != nil {
				t.Fatal(err)
			}
			receiptPath := filepath.Join(filepath.Dir(intentPath), relocationReceiptName(claim.ClaimID, intent.OperationID))
			if _, err := os.Lstat(receiptPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("fixture already completed: %v", err)
			}

			hit := false
			var cause error
			pending := func(home string, gotClaim workLogClaim, destination, branch, headSHA string) (*workLogRelocationIntent, string, error) {
				if destination != moved || gotClaim.ClaimID != claim.ClaimID || branch != claim.Branch || headSHA != head {
					t.Fatalf("pending binding=%+v %s %s %s", gotClaim, destination, branch, headSHA)
				}
				hit = true
				// The finalizer has already independently validated the active claim.
				// Mutations here exercise genuine native rereads in that race window.
				if kind == "journal changed" {
					if err := os.WriteFile(intentPath, []byte("{changed"), 0600); err != nil {
						t.Fatal(err)
					}
					got, path, err := pendingRelocationIntent(home, gotClaim, destination, branch, headSHA)
					cause = err
					return got, path, err
				}
				got, path, err := pendingRelocationIntent(home, gotClaim, destination, branch, headSHA)
				if err != nil || got == nil {
					t.Fatalf("valid native pending=%+v %v", got, err)
				}
				gitTest(t, moved, "remote", "set-url", "origin", fixture.oldRemote)
				return got, path, err
			}
			options.beforeWorkLogCompletion = nil
			receipts, err := finalizeRepositoryTransferWorkLogsWithPending(context.Background(), options, pending)
			if !hit || err == nil || receipts != nil {
				t.Fatalf("native finalizer refusal=%v %v hit=%v", receipts, err, hit)
			}
			if kind == "journal changed" {
				if cause == nil || !errors.Is(err, cause) {
					t.Fatalf("native pending cause=%v err=%v", cause, err)
				}
				if raw, readErr := os.ReadFile(intentPath); readErr != nil || string(raw) != "{changed" {
					t.Fatalf("changed journal evidence=%q %v", raw, readErr)
				}
			} else {
				if !strings.Contains(err.Error(), "relocated repository origin does not identify") {
					t.Fatalf("origin refusal=%v", err)
				}
				if raw, readErr := os.ReadFile(intentPath); readErr != nil || string(raw) != string(before) {
					t.Fatalf("intent changed=%q %v", raw, readErr)
				}
				if got := gitTestOutput(t, moved, "remote", "get-url", "origin"); got != fixture.oldRemote {
					t.Fatalf("origin evidence=%s", got)
				}
			}
			if _, err := os.Lstat(receiptPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected completion=%v", err)
			}
			if got := gitTestOutput(t, moved, "rev-parse", "HEAD"); got != head {
				t.Fatalf("moved HEAD=%s", got)
			}
		})
	}
}
