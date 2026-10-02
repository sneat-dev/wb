package sessionlaunch

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDependenciesRetainExecutableValidation(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if _, err := defaultDependenciesWithLookPath(directory, func(name string) (string, error) { return directory, nil }); err == nil {
		t.Fatal("tmux directory accepted as executable")
	}
}

func TestPreflightRetainsExecutableBoundaryFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"harness path", "WB lookup"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			deps := slCovFakeDeps(t, &fakeTmux{})
			refused := errors.New("WB executable unavailable")
			if stage == "harness path" {
				directory := t.TempDir()
				deps.lookPath = func(name string) (string, error) { return directory, nil }
			} else {
				deps.wbExecutable = func() (string, error) { return "", refused }
			}
			err := preflightLocalWithDependencies(RuntimeCodex, deps)
			if err == nil {
				t.Fatal("invalid executable boundary accepted")
			}
			if stage == "WB lookup" && !errors.Is(err, refused) {
				t.Fatalf("WB cause = %v", err)
			}
		})
	}
}

func TestPinnedWorktreeRejectsInvalidResolvedGitExecutable(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	err := verifyPinnedWorktreeWithGit(context.Background(), slCovPlan("handoff-validation"), func(name string) (string, error) { return directory, nil }, launchGitOutput)
	if err == nil {
		t.Fatal("git directory accepted as executable")
	}
}

func TestLauncherDirectoryObservationRetainsGetwdError(t *testing.T) {
	t.Parallel()
	refused := errors.New("working directory unavailable")
	err := verifyLauncherWorktreeWithDirectory(launchPlan{}, completeLaunchTestRequest(t), sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs")), func() (string, error) { return "", refused }, os.Stat)
	if !errors.Is(err, refused) {
		t.Fatalf("current directory = %v", err)
	}
}

func TestLauncherDirectoryObservationRetainsMissingTarget(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	plan := launchPlan{WorktreeDir: filepath.Join(cwd, "missing")}
	err := verifyLauncherWorktreeWithDirectory(plan, completeLaunchTestRequest(t), sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs")), func() (string, error) { return cwd, nil }, os.Stat)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing target = %v", err)
	}
}

func TestParkedLauncherDirectoryObservationFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"cwd", "target stat", "unsupported harness", "continuation argv"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			var mutate func(*sessionpark.Bundle)
			if stage == "continuation argv" {
				mutate = func(bundle *sessionpark.Bundle) { bundle.Continuation = "local" }
			}
			state, plan, bundle := parkedLaunchForBoundaryTest(t, mutate)
			refused := errors.New("current directory unavailable")
			getwd := func() (string, error) { return plan.WorktreeDir, nil }
			if stage == "cwd" {
				getwd = func() (string, error) { return "", refused }
			}
			stat := os.Stat
			if stage == "target stat" {
				stat = func(path string) (os.FileInfo, error) {
					if path == plan.WorktreeDir {
						return nil, refused
					}
					return os.Stat(path)
				}
			}
			if stage == "unsupported harness" {
				bundle.Source.Runtime = "unsupported"
				raw, err := sessionpark.EncodeBundle(bundle)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(plan.StoreRoot, plan.HandoffID, sessionpark.BundleFileName), raw, 0600); err != nil {
					t.Fatal(err)
				}
				plan.RequestDigest = sessionmove.DigestBytes(raw)
			}
			_, err := validatePrivateParkPlanWithDirectory(state, plan, getwd, stat)
			if err == nil {
				t.Fatal("invalid parked launcher boundary accepted")
			}
			if stage != "unsupported harness" && stage != "continuation argv" && !errors.Is(err, refused) {
				t.Fatalf("observation cause = %v", err)
			}
		})
	}
}

func parkedLaunchForBoundaryTest(t *testing.T, mutate func(*sessionpark.Bundle)) (*launchState, launchPlan, sessionpark.Bundle) {
	t.Helper()
	bundle := slCovLocalBundle()
	if mutate != nil {
		mutate(&bundle)
	}
	store := sessionpark.NewStore(filepath.Join(t.TempDir(), sessionpark.SourceDirName))
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock, err := store.Acquire(context.Background(), bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if _, _, err := store.PrepareLocalUnderLock(lock, slCovParkNow); err != nil {
		t.Fatal(err)
	}
	path, continuation, err := store.EnsureLocalSuccessorContextUnderLock(lock, nil)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := store.LocalLaunchRootUnderLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sessionpark.EncodeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	authority, err := sessionpark.LocalLaunchAuthority(bundle, digest, path, continuation)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := harnessSpecForAuthority(authority, worktree)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	plan := launchPlan{SchemaVersion: launchSchemaVersion, HandoffID: authority.AggregateID, RequestDigest: digest,
		SuccessorWBSessionID: authority.SuccessorWBSessionID, PredecessorWBSessionID: authority.PredecessorWBSessionID,
		Machine: authority.TargetMachine, TmuxName: "wb-session-" + authority.SuccessorWBSessionID, Runtime: spec.Runtime, Model: spec.Model,
		StoreRoot: store.Root, WorktreeDir: worktree, RootMode: string(authority.RootMode), HandoverPath: path, AuthorityFile: sessionpark.BundleFileName,
		ContinuationKind: string(authority.ContinuationKind), ContinuationDigest: sessionmove.Digest(authority.ContinuationDigest),
		WBExecutable: slCovExecutable(t, bin, "wb"), HarnessExecutable: slCovExecutable(t, bin, spec.Executable), HarnessArgs: spec.Args}
	state, err := openLaunchState(store.Root, bundle.ParkedSessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	return state, plan, bundle
}
