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

	"github.com/sneat-dev/wb/internal/gitremote"
)

//nolint:paralleltest // The existing native checkpoint fixture sets process-wide WB/Git environment.
func TestE2ESessionCheckpointReceiveWorkLogRereadRefusals(t *testing.T) {
	for _, stage := range []string{"run disappeared", "claim disappeared", "events unreadable", "no owner", "new owner", "inspection refused", "unchanged"} {
		//nolint:paralleltest // Each case uses the existing environment-owning fixture.
		t.Run(stage, func(t *testing.T) {
			fixture, worktree, source := newSessionCheckpointFixture(t, "checkpoint-reread-"+strings.ReplaceAll(stage, " ", "-"))
			reads := nativeSessionWorkLogReads()
			home, err := wbhomeRootForTest(fixture.projectsRoot)
			if err != nil {
				t.Fatal(err)
			}
			claim, _, _, err := activeWorkLogClaim(home, worktree)
			if err != nil {
				t.Fatal(err)
			}
			claimPath := filepath.Join(home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims", claim.ClaimID+".json")
			original, err := os.ReadFile(claimPath)
			if err != nil {
				t.Fatal(err)
			}
			observed := false
			switch stage {
			case "inspection refused":
				source.PID++
				observed = true
			case "run disappeared":
				reads.openRun = func(home, effort, run string, create bool) (*os.File, string, error) {
					observed = true
					root := filepath.Join(home, "worklogs", effort, "runs", run)
					if err := os.Rename(root, root+".moved"); err != nil {
						t.Fatal(err)
					}
					return openWorkLogRun(home, effort, run, create)
				}
			case "claim disappeared":
				reads.readClaim = func(directory *os.File, id string) (workLogClaim, error) {
					observed = true
					if err := os.Remove(claimPath); err != nil {
						t.Fatal(err)
					}
					return readWorkLogClaimAt(directory, id)
				}
			case "events unreadable", "no owner", "new owner":
				reads.readEvents = func(root string) ([]LocalWorkLogEvent, error) {
					observed = true
					events, err := readLocalEvents(root)
					if err != nil {
						t.Fatal(err)
					}
					var contents []byte
					sequence := 0
					if stage == "events unreadable" {
						contents = []byte("{invalid\n")
					} else {
						for _, event := range events {
							if event.Type == LocalEventOwner && event.Owner != nil {
								if stage == "no owner" {
									continue
								}
								owner := *event.Owner
								owner.PID = source.PID + 1
								event.Owner = &owner
							}
							event.Seq = sequence
							sequence++
							raw, err := json.Marshal(event)
							if err != nil {
								t.Fatal(err)
							}
							contents = append(contents, raw...)
							contents = append(contents, '\n')
						}
					}
					file := filepath.Join(root, journalRootDirectory, journalLocalDirectory, worklogDirectory, "events.jsonl")
					if err := os.WriteFile(file, contents, 0o600); err != nil {
						t.Fatal(err)
					}
					return readLocalEvents(root)
				}
			}
			reference, ownerID, err := parkedSessionWorkLogSnapshotWithReads(fixture.projectsRoot, worktree, source, reads)
			if stage == "unchanged" {
				if err != nil || reference == "" || ownerID == "" {
					t.Fatalf("snapshot = %q %q %v", reference, ownerID, err)
				}
			} else {
				if !observed || err == nil || reference != "" || ownerID != "" {
					t.Fatalf("%s = %q %q %v, observed=%t", stage, reference, ownerID, err, observed)
				}
				if strings.Contains(stage, "disappeared") && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("native disappeared error = %v", err)
				}
				if stage == "events unreadable" && !strings.Contains(err.Error(), "inspect exact parked owner event") {
					t.Fatalf("read stage = %v", err)
				}
				if (stage == "no owner" || stage == "new owner") && !strings.Contains(err.Error(), "exact latest parked owner") {
					t.Fatalf("owner stage = %v", err)
				}
			}
			if stage != "claim disappeared" && stage != "run disappeared" {
				got, err := os.ReadFile(claimPath)
				if err != nil || string(got) != string(original) {
					t.Fatalf("immutable claim changed: %v", err)
				}
			}
		})
	}
}

//nolint:paralleltest // The existing native checkpoint fixture sets process-wide WB/Git environment.
func TestE2ESessionCheckpointReceivePreflightRechecksNativeEvidence(t *testing.T) {
	for _, stage := range []string{"canonical replaced", "worktree replaced", "status fails", "branch changed", "HEAD changed", "dirty", "fetch changed", "push changed", "unchanged"} {
		//nolint:paralleltest // Cases construct independent environment-owning fixtures.
		t.Run(stage, func(t *testing.T) {
			fixture, root, _ := newSessionCheckpointFixture(t, "checkpoint-recheck-"+strings.ReplaceAll(stage, " ", "-"))
			canonical := mustOpenCanonical(t, fixture.canonical)
			held, err := openAdoptedCleanupWorktree(root)
			if err != nil {
				t.Fatal(err)
			}
			preflight := &sessionCheckpointPreflight{root: root, canonical: canonical, worktree: held,
				branch: gitTestOutput(t, root, "symbolic-ref", "--quiet", "--short", "HEAD"), sourceCommit: gitTestOutput(t, root, "rev-parse", "--verify", "HEAD^{commit}"),
				repositoryRemote: gitTestOutput(t, fixture.canonical, "remote", "get-url", "--all", "origin"), pushRemote: gitTestOutput(t, fixture.canonical, "remote", "get-url", "--all", "--push", "origin")}
			t.Cleanup(preflight.close)
			query := git
			statusObserved := false
			var statusNativeError error
			switch stage {
			case "canonical replaced":
				if err := os.Rename(fixture.canonical, fixture.canonical+".moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(fixture.canonical, 0o700); err != nil {
					t.Fatal(err)
				}
			case "worktree replaced":
				if err := os.Rename(root, root+".moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
			case "branch changed":
				gitTest(t, root, "branch", "-m", "changed-checkpoint-branch")
			case "HEAD changed":
				gitTest(t, root, "commit", "--allow-empty", "-m", "moved checkpoint HEAD")
			case "dirty":
				if err := os.WriteFile(filepath.Join(root, "untracked-checkpoint.txt"), []byte("retain me\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "fetch changed":
				gitTest(t, fixture.canonical, "remote", "set-url", "origin", "https://github.com/acme/changed.git")
			case "push changed":
				gitTest(t, fixture.canonical, "remote", "set-url", "--push", "origin", "https://github.com/acme/changed.git")
			case "status fails":
				query = func(ctx context.Context, root string, args ...string) (string, error) {
					if args[0] == "status" {
						statusObserved = true
						missing := filepath.Join(t.TempDir(), "missing-native-metadata")
						if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+missing+"\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					value, nativeErr := git(ctx, root, args...)
					if args[0] == "status" {
						statusNativeError = nativeErr
					}
					return value, nativeErr
				}
			}
			if stage == "unchanged" {
				err = verifySessionCheckpointUnchanged(t.Context(), preflight)
			} else {
				err = verifySessionCheckpointUnchangedWithGit(t.Context(), preflight, query)
			}
			if stage == "status fails" && (!statusObserved || statusNativeError == nil) {
				t.Fatalf("native status query observed=%t, error=%v", statusObserved, statusNativeError)
			}
			if stage == "unchanged" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			want := map[string]string{"canonical replaced": "canonical repository changed", "worktree replaced": "source worktree changed", "status fails": "source worktree changed", "branch changed": "source named branch changed", "HEAD changed": "source HEAD changed", "dirty": "source worktree changed", "fetch changed": "origin fetch remote changed", "push changed": "origin push remote changed"}[stage]
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s = %v, want %q", stage, err, want)
			}
		})
	}
}

//nolint:paralleltest // The existing receive fixture sets process-wide WB/Git environment.
func TestE2ESessionCheckpointReceiveAdmissionRetainsLayoutFailures(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	spec := sessionReceiveStateSpec(fixture)
	for _, host := range []string{"github.com", "gitlab.com"} {
		if err := os.MkdirAll(filepath.Join(fixture.projectsRoot, host, "acme", "app", ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SessionReceiveMemberPath(fixture.projectsRoot, spec); err == nil || !strings.Contains(err.Error(), "more than one host") {
		t.Fatalf("path ambiguity = %v", err)
	}
	fence, digest := acquireSessionReceiveFence(t, fixture, fixture.home)
	spec.AuthorityStore = fixture.home
	spec.AuthorityDigest = digest
	spec.Fence = fence
	if _, err := VerifyReceivedSessionMember(t.Context(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec}); err == nil || !strings.Contains(err.Error(), "more than one host") {
		t.Fatalf("replay ambiguity = %v", err)
	}
	state := &sessionReceiveState{}
	t.Cleanup(state.close)
	if err := state.prepareSource(t.Context(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec}, nil); err == nil || !strings.Contains(err.Error(), "more than one host") {
		t.Fatalf("source ambiguity = %v", err)
	}
	fixture.requireNoTargetWorktree(t)
	for _, host := range []string{"github.com", "gitlab.com"} {
		if err := os.RemoveAll(filepath.Join(fixture.projectsRoot, host)); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	err := state.prepareSourceWithAdmission(t.Context(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec}, nil, CanonicalRepositoryPathForURL, func() error { calls++; return os.ErrPermission })
	if calls != 1 || !errors.Is(err, os.ErrPermission) || state.canonical != nil {
		t.Fatalf("capability refusal = %v, calls=%d canonical=%v", err, calls, state.canonical)
	}
	fixture.requireNoTargetWorktree(t)
}

//nolint:paralleltest // The existing receive fixture sets process-wide WB/Git environment.
func TestE2ESessionCheckpointReceiveCanonicalRejectsLinkedMetadata(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	canonical := mustOpenCanonical(t, fixture.canonical)
	declared, err := gitremote.Parse(fixture.remote)
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", "line-one\nline-two")
	if _, err := readCanonicalOriginRemote(t.Context(), canonical, false); err == nil || !strings.Contains(err.Error(), "one safe value") {
		t.Fatalf("multiline native origin = %v", err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", fixture.remote)
	linked := filepath.Join(fixture.root, "linked")
	gitTest(t, fixture.canonical, "worktree", "add", "--detach", linked, fixture.request.SourceWorkCommit)
	ctx := withCanonicalGitInterceptor(t.Context(), func(ctx context.Context, args []string, next func() ([]byte, error)) ([]byte, error) {
		if args[0] == "rev-parse" && args[1] != "--show-toplevel" {
			// Observe a real linked checkout's Git/common directories. The verifier
			// must refuse this metadata even when the root observation was canonical.
			value, err := git(ctx, linked, args...)
			return []byte(value + "\n"), err
		}
		return next()
	})
	if err := verifySessionReceiveCanonical(ctx, canonical, declared.Identity); err == nil || !strings.Contains(err.Error(), "linked worktree") {
		t.Fatalf("linked metadata = %v", err)
	}
	rootCtx := withCanonicalGitInterceptor(t.Context(), func(ctx context.Context, args []string, next func() ([]byte, error)) ([]byte, error) {
		if len(args) == 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel" {
			value, err := git(ctx, linked, args...)
			return []byte(value + "\n"), err
		}
		return next()
	})
	if err := verifySessionReceiveCanonical(rootCtx, canonical, declared.Identity); err == nil || !strings.Contains(err.Error(), "not the root") {
		t.Fatalf("linked root query = %v", err)
	}
	assertSessionReceiveEvidenceUnchanged(t, fixture, "")
}

//nolint:paralleltest // The existing receive fixture sets process-wide WB/Git environment.
func TestE2ESessionCheckpointReceiveHeldQueriesRefuseNativeDrift(t *testing.T) {
	for _, stage := range []string{"root unavailable", "root mismatch", "HEAD unavailable", "HEAD moved", "status unavailable", "unchanged"} {
		//nolint:paralleltest // Each case uses an independent environment-owning fixture.
		t.Run(stage, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			created, err := ReceiveSessionBundle(t.Context(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request})
			if err != nil {
				t.Fatal(err)
			}
			handle, err := openAdoptedCleanupWorktree(created.WorktreeDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(handle.close)
			pin := "wb-session/" + fixture.request.HandoffID
			if stage == "root unavailable" {
				if err := handle.worktree.Close(); err != nil {
					t.Fatal(err)
				}
			}
			path := created.WorktreeDir
			if stage == "root mismatch" {
				path += ".different"
			}
			queries := []string{}
			query := func(ctx context.Context, args ...string) ([]byte, error) {
				queries = append(queries, strings.Join(args, " "))
				if stage == "HEAD unavailable" && len(args) == 3 && args[0] == "rev-parse" && args[2] == "HEAD^{commit}" {
					headPath := gitTestOutput(t, created.WorktreeDir, "rev-parse", "--git-path", "HEAD")
					if !filepath.IsAbs(headPath) {
						headPath = filepath.Join(created.WorktreeDir, headPath)
					}
					if err := os.Remove(headPath); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "HEAD moved" && len(args) == 3 && args[0] == "rev-parse" && args[2] == "HEAD^{commit}" {
					gitTest(t, fixture.canonical, "update-ref", "refs/heads/"+pin, fixture.request.SourceWorkCommit)
				}
				if stage == "status unavailable" && args[0] == "status" {
					if err := os.Remove(filepath.Join(created.WorktreeDir, ".git")); err != nil {
						t.Fatal(err)
					}
				}
				return runSecureRenameGitBytesWithHeldWorktree(ctx, fixture.physicalCanonical(), fixture.physicalWorktreesRoot(), created.WorktreeDir, handle.worktree, args...)
			}
			err = verifyHeldSessionReceiveCheckoutWithQuery(t.Context(), path, pin, fixture.request.BundleCommit, query)
			if stage == "unchanged" {
				if err != nil {
					t.Fatal(err)
				}
				want := []string{"rev-parse --show-toplevel", "symbolic-ref --quiet HEAD", "rev-parse --verify refs/heads/" + pin + "^{commit}", "rev-parse --verify HEAD^{commit}", "status --porcelain=v1 --untracked-files=all"}
				if strings.Join(queries, "\n") != strings.Join(want, "\n") {
					t.Fatalf("query order = %q, want %q", queries, want)
				}
				assertSessionReceiveEvidenceUnchanged(t, fixture, created.WorktreeDir)
				return
			}
			want := map[string]string{"root unavailable": "verify existing target linked worktree common directory", "root mismatch": "root does not match", "HEAD unavailable": "verify existing target HEAD", "HEAD moved": "want exact bundle commit", "status unavailable": "inspect existing target worktree status"}[stage]
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s = %v, want %q; queries=%q", stage, err, want, queries)
			}
			if got := gitTestOutput(t, fixture.remote, "rev-parse", "refs/heads/"+fixture.request.Branch); got != fixture.request.BundleCommit {
				t.Fatalf("refusal changed remote source ref: %s", got)
			}
			if stage == "HEAD moved" {
				if got := gitTestOutput(t, fixture.canonical, "rev-parse", "refs/heads/"+pin); got != fixture.request.SourceWorkCommit {
					t.Fatalf("refusal overwrote moved pin: %s", got)
				}
			}
		})
	}
}

//nolint:paralleltest // The existing receive fixture sets process-wide WB/Git environment.
func TestE2ESessionCheckpointReceiveRenormalizedRootRefusesBeforeCapability(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	state := &sessionReceiveState{}
	t.Cleanup(state.close)
	original, normalizeErr := absoluteProjectsRoot(fixture.projectsRoot)
	if normalizeErr != nil {
		t.Fatal(normalizeErr)
	}
	moved := original + ".moved"
	calls := 0
	resolve := func(root, repository, remote string) (string, error) {
		if root != original {
			t.Fatalf("first normalization = %q, want %q", root, original)
		}
		if err := os.Rename(original, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, original); err != nil {
			t.Fatal(err)
		}
		// Use the native resolver: its second normalization now observes the new
		// resolved root. A caller-bound root must not silently acquire this path.
		return CanonicalRepositoryPathForURL(root, repository, remote)
	}
	err := state.prepareSourceWithAdmission(t.Context(), SessionMemberReceiveOptions{ProjectsRoot: original, Spec: sessionReceiveStateSpec(fixture)}, nil, resolve, func() error { calls++; return requireGitFilesystemCapability() })
	if err == nil || !strings.Contains(err.Error(), "not below the projects root") || calls != 0 || state.canonical != nil {
		t.Fatalf("root swap = %v, capability calls=%d canonical=%v", err, calls, state.canonical)
	}
	if got := gitTestOutput(t, filepath.Join(moved, "acme", "app"), "rev-parse", "HEAD"); got != fixture.request.SourceWorkCommit {
		t.Fatalf("refusal changed retained clone: %s", got)
	}
	if info, err := os.Lstat(original); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("replacement root changed: %v %v", info, err)
	}
	fixture.requireNoTargetWorktree(t)
}

//nolint:paralleltest // The existing receive fixture sets process-wide WB/Git environment.
func TestE2ESessionCheckpointReceiveNativeFetchEvidenceDisappearance(t *testing.T) {
	for _, stage := range []string{"remote disappeared", "fetched ref disappeared"} {
		//nolint:paralleltest // Each case has an independent environment-owning fixture.
		t.Run(stage, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			spec := sessionReceiveStateSpec(fixture)
			state := &sessionReceiveState{}
			t.Cleanup(state.close)
			observed := false
			ctx := t.Context()
			var after func()
			if stage == "remote disappeared" {
				after = func() {
					observed = true
					if err := os.Rename(fixture.remote, fixture.remote+".moved"); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				fetchedRef := sessionReceiveFetchRef(spec.OperationID)
				ctx = withCanonicalGitInterceptor(ctx, func(_ context.Context, args []string, next func() ([]byte, error)) ([]byte, error) {
					if len(args) == 3 && args[0] == "rev-parse" && args[1] == "--verify" && args[2] == fetchedRef+"^{commit}" {
						observed = true
						if got := gitTestOutput(t, fixture.canonical, "rev-parse", "--verify", fetchedRef); got != spec.Commit {
							t.Fatalf("fetched native tip = %q, want %q", got, spec.Commit)
						}
						gitTest(t, fixture.canonical, "update-ref", "-d", fetchedRef)
					}
					return next()
				})
			}
			err := state.prepareSource(ctx, SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec}, after)
			want := map[string]string{"remote disappeared": "fetch live session branch", "fetched ref disappeared": "resolve live fetched branch tip"}[stage]
			if !observed || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s = %v, observed=%t", stage, err, observed)
			}
			if stage == "remote disappeared" {
				if err := os.Rename(fixture.remote+".moved", fixture.remote); err != nil {
					t.Fatal(err)
				}
			}
			assertSessionReceiveEvidenceUnchanged(t, fixture, "")
		})
	}
}
