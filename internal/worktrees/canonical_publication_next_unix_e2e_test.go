//go:build !windows && e2e

package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestE2ECanonicalPublicationOwnedDescriptors(t *testing.T) {
	t.Parallel()
	var absent *canonicalRepository
	absent.close()
	for _, canonical := range []*canonicalRepository{nil, {}, {root: &os.File{}}, {common: &os.File{}}} {
		if err := canonical.validate(); err == nil || !strings.Contains(err.Error(), "descriptors are unavailable") {
			t.Fatalf("partial descriptors = %v", err)
		}
	}
	canonical := reconciliationFakeCanonical(t)
	if err := canonical.validate(); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(canonical.path, ".git")
	retained := original + ".retained"
	if err := os.Rename(original, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	if err := canonical.validate(); err == nil || !strings.Contains(err.Error(), "canonical Git directory changed") {
		t.Fatalf("replacement admitted: %v", err)
	}
	if _, err := canonical.common.Stat(); err != nil {
		t.Fatalf("retained common descriptor lost: %v", err)
	}
	canonical.close()
	for _, directory := range []*os.File{canonical.root, canonical.common} {
		if _, err := directory.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("owned descriptor not closed: %v", err)
		}
	}
	if info, err := os.Stat(retained); err != nil || !info.IsDir() {
		t.Fatalf("retained evidence changed: %v, %v", info, err)
	}
}

func TestE2ECanonicalPublicationHeldPathResponseAdmission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	directory := wtLifeCovOpenDirectory(t, root)
	got, err := secureDirectoryPath(ctx, directory)
	if err != nil || got != root {
		t.Fatalf("native held path = %q, %v", got, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := secureDirectoryPath(cancelled, directory); err == nil || got != "" || !strings.Contains(err.Error(), "derive held directory path: context canceled") {
		t.Fatalf("cancelled native child launch = %q, %v", got, err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := secureDirectoryPath(ctx, directory); err == nil || got != "" || !strings.Contains(err.Error(), "derive held directory path") {
		t.Fatalf("closed native held path = %q, %v", got, err)
	}
	// Defensive child-response admission; the native path helper has not been
	// observed returning a relative pathname.
	if got, err := admitSecureDirectoryPath([]byte("relative\n"), nil); err == nil || got != "" || !strings.Contains(err.Error(), "held directory path is not absolute") {
		t.Fatalf("relative response = %q, %v", got, err)
	}
	if got, err := admitSecureDirectoryPath([]byte("specific child refusal\n"), os.ErrPermission); err == nil || got != "" || err.Error() != "derive held directory path: specific child refusal" {
		t.Fatalf("child diagnostic = %q, %v", got, err)
	}
}

//nolint:paralleltest // The existing native repository fixture pins HOME and WB/XDG roots with t.Setenv.
func TestE2ECanonicalPublicationGuardAdmissionRefusals(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()
	worktree := filepath.Join(fixture.canonical, ".worktrees", "publication-admission")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/publication-admission", worktree, "main")
	options := GuardOptions{ProjectsRoot: fixture.projectsRoot, Base: "main"}
	before := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	guarded, err := Guard(ctx, worktree, options)
	if err != nil || guarded.Kind != "linked" {
		t.Fatalf("initial native linked admission = %+v, %v", guarded, err)
	}
	//nolint:paralleltest // The ancestor newGitFixture pins HOME/WB/XDG with t.Setenv; these refusals share its linked checkout and must remain serial.
	t.Run("invalid base", func(t *testing.T) {
		bad := options
		bad.Base = "bad branch"
		got, err := Guard(ctx, worktree, bad)
		if err == nil || !reflect.DeepEqual(got, GuardResult{}) || !strings.Contains(err.Error(), "base branch") {
			t.Fatalf("invalid base = %+v, %v", got, err)
		}
	})
	//nolint:paralleltest // The ancestor newGitFixture pins HOME/WB/XDG with t.Setenv; these refusals share its linked checkout and must remain serial.
	t.Run("missing own claim", func(t *testing.T) {
		bad := options
		bad.Admission = AdmissionEnforce
		expected := CheckAdmission(worktree, AdmissionEnforce)
		if expected.Admitted {
			t.Fatal("unclaimed fixture unexpectedly admitted")
		}
		got, err := Guard(ctx, worktree, bad)
		if err == nil || !reflect.DeepEqual(got, GuardResult{}) || !strings.Contains(err.Error(), expected.Reason) {
			t.Fatalf("missing claim = %+v, %v", got, err)
		}
	})
	//nolint:paralleltest // The ancestor newGitFixture pins HOME/WB/XDG with t.Setenv; these refusals share its linked checkout and must remain serial.
	t.Run("invalid configured store", func(t *testing.T) {
		config := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", config)
		mustWriteBranchConfig(t, filepath.Join(config, "wb", "worktrees.yaml"), "version: [\n")
		got, err := Guard(ctx, worktree, options)
		if err == nil || !reflect.DeepEqual(got, GuardResult{}) || !strings.Contains(err.Error(), "parse worktrees config") {
			t.Fatalf("malformed configured store = %+v, %v", got, err)
		}
	})
	if after := gitTestOutput(t, worktree, "rev-parse", "HEAD"); after != before {
		t.Fatalf("refusal changed HEAD %s -> %s", before, after)
	}
}

//nolint:paralleltest // The native repository fixture scopes process-wide HOME and WB/XDG configuration.
func TestE2ECanonicalPublicationResumeClaimSelection(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(canonical.close)
	ctx := context.Background()
	task := "resume-authority"
	makeWorktree := func(name, claimTask, run string) string {
		worktree := filepath.Join(fixture.canonical, ".worktrees", name)
		branch := "feature/" + name
		gitTest(t, fixture.canonical, "worktree", "add", "-b", branch, worktree, "main")
		head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
		outcome, err := recordWorkLogWithHooks(fixture.home, claimTask, CreateResult{Repository: "acme/app", CanonicalDir: fixture.canonical, WorktreeDir: worktree, Branch: branch, Base: "main", BaseSHA: head}, WorkLogOptions{EffortID: claimTask, RunID: run, AgentID: "codex", Model: "unknown"}, workLogPublicationHooks{})
		if err != nil || !outcome.ClaimWritten || !outcome.ProjectionWritten {
			t.Fatalf("native claim publication = %+v, %v", outcome, err)
		}
		claim, _, _, err := activeWorkLogClaim(fixture.home, worktree)
		if err != nil || claim.Task != claimTask || claim.Repository != "acme/app" {
			t.Fatalf("native claim prerequisite = %+v, %v", claim, err)
		}
		return worktree
	}
	unrelated := makeWorktree("unrelated-resume", "other-task", "other-run")
	predicted := filepath.Join(fixture.canonical, ".worktrees", task)
	got, err := locateResumableWorktree(ctx, canonical, fixture.home, task, "acme/app", predicted)
	if err != nil || got != "" {
		t.Fatalf("unrelated intact claim selected = %q, %v", got, err)
	}
	first := makeWorktree("first-resume", task, "first-run")
	got, err = locateResumableWorktree(ctx, canonical, fixture.home, task, "acme/app", predicted)
	if err != nil || got != first {
		t.Fatalf("sole intact native claim = %q, %v", got, err)
	}
	second := makeWorktree("second-resume", task, "second-run")
	got, err = locateResumableWorktree(ctx, canonical, fixture.home, task, "acme/app", predicted)
	if err == nil || got != "" || !strings.Contains(err.Error(), "active work-log claims select more than one worktree") {
		t.Fatalf("ambiguous intact claims = %q, %v", got, err)
	}
	for _, path := range []string{unrelated, first, second} {
		if _, _, _, err := activeWorkLogClaim(fixture.home, path); err != nil {
			t.Fatalf("selection changed claim %s: %v", path, err)
		}
	}
}

func TestE2ECanonicalPublicationLegacyResumeAndUnsafeCandidate(t *testing.T) {
	t.Parallel()
	repo := wtLifeCovNewRepo(t)
	canonical := &canonicalRepository{path: repo.path, root: repo.root, common: repo.common}
	home := t.TempDir()
	predicted := filepath.Join(t.TempDir(), "legacy")
	gitTest(t, repo.path, "worktree", "add", "-b", "feature/legacy", predicted, "main")
	got, err := locateResumableWorktree(context.Background(), canonical, home, "legacy", "acme/app", predicted)
	if err != nil || got != predicted {
		t.Fatalf("native legacy predicted checkout = %q, %v", got, err)
	}
	unsafe := filepath.Join(t.TempDir(), "candidate")
	if err := os.WriteFile(unsafe, []byte("retain candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = locateResumableWorktree(context.Background(), canonical, home, "unsafe", "acme/app", unsafe)
	if err == nil || got != "" {
		t.Fatalf("non-directory predicted candidate = %q, %v", got, err)
	}
	if contents, err := os.ReadFile(unsafe); err != nil || string(contents) != "retain candidate" {
		t.Fatalf("unsafe candidate changed = %q, %v", contents, err)
	}
}

func TestE2ECanonicalPublicationRegistrationAuthority(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"success", "final substitution", "query refusal", "missing baseline", "missing final", "branch query refusal", "wrong branch", "transient sibling"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := wtLifeCovNewRepo(t)
			canonical := &canonicalRepository{path: repo.path, root: repo.root, common: repo.common}
			ownerPath := t.TempDir()
			owner := wtLifeCovOpenDirectory(t, ownerPath)
			finalPath := filepath.Join(ownerPath, "checkout")
			gitTest(t, repo.path, "worktree", "add", "-b", "feature/publication", finalPath, "main")
			final := wtLifeCovOpenDirectory(t, finalPath)
			ctx := context.Background()
			baseline := map[string]bool{repo.path: true}
			branch := "feature/publication"
			want := ""
			switch name {
			case "final substitution":
				retained := finalPath + ".retained"
				if err := os.Rename(finalPath, retained); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(finalPath, 0700); err != nil {
					t.Fatal(err)
				}
				want = "published worktree path changed"
			case "query refusal", "branch query refusal":
				_, cause := os.ReadFile(filepath.Join(t.TempDir(), "native-query-cause"))
				if !errors.Is(cause, os.ErrNotExist) {
					t.Fatalf("native query cause = %v", cause)
				}
				queries := 0
				// Existing query-boundary refusal carrying a native cause, not a claim
				// that Git spontaneously returned this particular filesystem error.
				ctx = withCanonicalGitInterceptor(ctx, func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
					if reflect.DeepEqual(args, []string{"worktree", "list", "--porcelain"}) {
						queries++
						if name == "query refusal" || queries == 2 {
							return nil, cause
						}
					}
					return run()
				})
				want = "list worktree registrations"
			case "missing baseline":
				peer := filepath.Join(ownerPath, "peer")
				gitTest(t, repo.path, "worktree", "add", "-b", "feature/peer", peer, "main")
				baseline[peer] = true
				gitTest(t, repo.path, "worktree", "remove", peer)
				want = "existing worktree registration disappeared"
			case "missing final":
				gitTest(t, repo.path, "worktree", "remove", finalPath)
				// Recreate the exact current directory and retain it after the native
				// registration removal, so earlier path identity checks really pass.
				if err := os.Mkdir(finalPath, 0700); err != nil {
					t.Fatal(err)
				}
				if err := final.Close(); err != nil {
					t.Fatal(err)
				}
				final = wtLifeCovOpenDirectory(t, finalPath)
				want = "published worktree is not registered"
			case "wrong branch":
				branch = "feature/expected"
				want = "published worktree registration branch is"
			case "transient sibling":
				peer := filepath.Join(ownerPath, ".wb-stage-peer", "checkout")
				gitTest(t, repo.path, "worktree", "add", "-b", "feature/peer", peer, "main")
				baseline[peer] = true
				gitTest(t, repo.path, "worktree", "move", peer, filepath.Join(ownerPath, "peer-published"))
			}
			err := verifyPublishedWorktree(ctx, canonical, baseline, ownerPath, owner, finalPath, final, branch)
			if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
				t.Fatalf("publication %s = %v; want %q", name, err, want)
			}
			if _, err := owner.Stat(); err != nil {
				t.Fatalf("borrowed owner closed: %v", err)
			}
			if _, err := final.Stat(); err != nil {
				t.Fatalf("borrowed final closed: %v", err)
			}
			if got := gitTestOutput(t, repo.path, "rev-parse", "HEAD"); got != repo.head {
				t.Fatalf("canonical HEAD changed: %s", got)
			}
		})
	}
}

func TestE2ECanonicalPublicationExistingCheckoutIdentity(t *testing.T) {
	t.Parallel()
	repo := wtLifeCovNewRepo(t)
	canonical := &canonicalRepository{path: repo.path, root: repo.root, common: repo.common}
	checkout := filepath.Join(t.TempDir(), "checkout")
	gitTest(t, repo.path, "worktree", "add", "-b", "feature/existing", checkout, "main")
	nested := filepath.Join(checkout, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	other := wtLifeCovNewRepo(t)
	wrongCanonical := &canonicalRepository{path: other.path, root: other.root, common: other.common}
	for _, tc := range []struct {
		name, path, branch, diagnostic string
		canonical                      *canonicalRepository
	}{
		{"valid", checkout, "feature/existing", "", canonical},
		{"nested", nested, "feature/existing", "Git root is", canonical},
		{"canonical", repo.path, "main", "not a linked worktree", canonical},
		{"other canonical", checkout, "feature/existing", "belongs to", wrongCanonical},
		{"wrong branch", checkout, "feature/wanted", "on branch", canonical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateExistingWorktree(context.Background(), tc.canonical, tc.path, tc.branch)
			if tc.diagnostic == "" && err != nil || tc.diagnostic != "" && (err == nil || !strings.Contains(err.Error(), tc.diagnostic)) {
				t.Fatalf("existing checkout %s = %v", tc.name, err)
			}
		})
	}
	if got := gitTestOutput(t, checkout, "branch", "--show-current"); got != "feature/existing" {
		t.Fatalf("refusal changed branch: %s", got)
	}
	if got := gitTestOutput(t, checkout, "rev-parse", "HEAD"); got != repo.head {
		t.Fatalf("refusal changed HEAD: %s", got)
	}
}

func TestE2ECanonicalPublicationRegisteredBranchAdmission(t *testing.T) {
	t.Parallel()
	repo := wtLifeCovNewRepo(t)
	canonical := &canonicalRepository{path: repo.path, root: repo.root, common: repo.common}
	path := filepath.Join(t.TempDir(), "checkout")
	gitTest(t, repo.path, "worktree", "add", "-b", "feature/registered", path, "main")
	got, err := registeredBranchNameCanonical(context.Background(), canonical, path)
	if err != nil || got != "feature/registered" {
		t.Fatalf("native registered branch = %q, %v", got, err)
	}
	for _, branch := range []string{"refs/tags/tag", "refs/heads/bad branch"} {
		// Defensive arbitrary registration-response contract; native Git has not
		// been observed emitting an invalid branch name here.
		ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
			if reflect.DeepEqual(args, []string{"worktree", "list", "--porcelain"}) {
				return []byte("worktree " + path + "\nbranch " + branch + "\n"), nil
			}
			return run()
		})
		got, err := registeredBranchNameCanonical(ctx, canonical, path)
		if err == nil || got != "" || !strings.Contains(err.Error(), "registration is detached or has invalid branch") {
			t.Fatalf("registration response %q = %q, %v", branch, got, err)
		}
	}
	if got := gitTestOutput(t, path, "branch", "--show-current"); got != "feature/registered" {
		t.Fatalf("response admission changed actual branch: %s", got)
	}
}

func TestE2ECanonicalPublicationStagedGitNativeAuthority(t *testing.T) {
	t.Parallel()
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing-%t", existing), func(t *testing.T) {
			t.Parallel()
			repo := wtLifeCovNewRepo(t)
			canonical := &canonicalRepository{path: repo.path, root: repo.root, common: repo.common}
			operationRoot, stagePath, stage := wtLifeCovStage(t)
			branch := "feature/staged-publication"
			if existing {
				gitTest(t, repo.path, "branch", branch, "main")
			}
			ctx := withProjectsRoot(context.Background(), t.TempDir())
			if err := gitWorktreeAddFromStageDirectory(ctx, canonical, operationRoot, stage, branch, repo.head, existing); err != nil {
				t.Fatalf("native staged add = %v", err)
			}
			checkout := filepath.Join(stagePath, "checkout")
			if got := gitTestOutput(t, checkout, "branch", "--show-current"); got != branch {
				t.Fatalf("native added branch = %s", got)
			}
			if got := gitTestOutput(t, checkout, "rev-parse", "HEAD"); got != repo.head {
				t.Fatalf("native added HEAD = %s", got)
			}
			if err := gitWorktreeAddFromStageDirectory(ctx, nil, operationRoot, stage, branch, repo.head, existing); err == nil || !strings.Contains(err.Error(), "descriptors are unavailable") {
				t.Fatalf("staged authorization = %v", err)
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if err := gitWorktreeAddFromStageDirectory(cancelled, canonical, operationRoot, stage, "unused", repo.head, false); err == nil || !strings.Contains(err.Error(), "git worktree add in secure staging directory: context canceled") {
				t.Fatalf("cancelled staged native launch = %v", err)
			}
			if got := gitTestOutput(t, checkout, "rev-parse", "HEAD"); got != repo.head {
				t.Fatalf("failure changed staged HEAD = %s", got)
			}
		})
	}
}

func TestE2ECanonicalPublicationSynchronizationAdmission(t *testing.T) {
	t.Parallel()
	if got, err := synchronizeCanonicalWithHook(context.Background(), nil, "acme/app", "main", nil); got != "" || err == nil || !strings.Contains(err.Error(), "descriptors are unavailable") {
		t.Fatalf("unowned synchronization = %q, %v", got, err)
	}
	for _, name := range []string{"wrong root", "linked metadata"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := wtLifeCovNewRepo(t)
			canonical := &canonicalRepository{path: repo.path, root: repo.root, common: repo.common}
			linked := filepath.Join(t.TempDir(), "linked")
			gitTest(t, repo.path, "worktree", "add", "-b", "feature/sync", linked, "main")
			want := "not the root of its canonical clone"
			if name == "linked metadata" {
				want = "a linked worktree, not the canonical clone"
			}
			ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
				if len(args) > 0 && args[0] == "fetch" {
					t.Fatal("fetch ran before canonical admission refusal")
				}
				if name == "wrong root" && reflect.DeepEqual(args, []string{"rev-parse", "--show-toplevel"}) {
					return []byte(gitTestOutput(t, linked, args...) + "\n"), nil
				}
				if name == "linked metadata" && reflect.DeepEqual(args, []string{"rev-parse", "--absolute-git-dir"}) {
					return []byte(gitTestOutput(t, linked, args...) + "\n"), nil
				}
				return run()
			})
			// The boundary supplies real native linked-checkout query bytes; it does
			// not claim the retained canonical descriptor naturally changed identity.
			if got, err := synchronizeCanonicalWithHook(ctx, canonical, "acme/app", "main", nil); got != "" || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("synchronization %s = %q, %v", name, got, err)
			}
			if got := gitTestOutput(t, repo.path, "rev-parse", "HEAD"); got != repo.head {
				t.Fatalf("admission changed canonical HEAD = %s", got)
			}
		})
	}
}

func TestE2ECanonicalPublicationFetchRejectsDefensiveObjectResponse(t *testing.T) {
	t.Parallel()
	repo := wtLifeCovNewRepo(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", remote)
	gitTest(t, repo.path, "remote", "add", "origin", remote)
	gitTest(t, repo.path, "push", "origin", "main")
	privateRef := ""
	run := func(ctx context.Context, args ...string) (string, error) {
		output, err := git(ctx, repo.path, args...)
		if err != nil {
			return output, err
		}
		if len(args) > 0 && args[0] == "fetch" {
			refspec := args[len(args)-1]
			_, privateRef, _ = strings.Cut(refspec, ":")
		}
		if len(args) > 0 && args[0] == "rev-parse" {
			if !isGitObjectID(output) || output != repo.head {
				t.Fatalf("native fetched-object prerequisite = %q", output)
			}
			// Defensive raw response contract, not a claim that native Git emitted
			// an invalid object ID. Fetch and cleanup still execute native Git.
			return "invalid-object-id", nil
		}
		return output, nil
	}
	got, err := fetchOriginBranchToPrivateRef(context.Background(), "acme/app", "main", run, nil)
	if err == nil || got != "" || !strings.Contains(err.Error(), "git returned invalid commit") || privateRef == "" {
		t.Fatalf("invalid object response = %q, %v; ref %q", got, err, privateRef)
	}
	if refs := gitTestOutput(t, repo.path, "for-each-ref", "--format=%(refname)", privateRef); refs != "" {
		t.Fatalf("private fetch ref remained: %s", refs)
	}
	if head := gitTestOutput(t, repo.path, "rev-parse", "HEAD"); head != repo.head {
		t.Fatalf("defensive object refusal changed HEAD: %s", head)
	}
}

//nolint:paralleltest // Existing native repository fixture pins HOME and WB/XDG roots with t.Setenv.
func TestE2ECanonicalPublicationResumeMalformedRequestedClaim(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(canonical.close)
	task := "malformed-resume"
	path := filepath.Join(fixture.canonical, ".worktrees", task)
	branch := "feature/" + task
	gitTest(t, fixture.canonical, "worktree", "add", "-b", branch, path, "main")
	head := gitTestOutput(t, path, "rev-parse", "HEAD")
	outcome, err := recordWorkLogWithHooks(fixture.home, task, CreateResult{Repository: "acme/app", CanonicalDir: fixture.canonical, WorktreeDir: path, Branch: branch, Base: "main", BaseSHA: head}, WorkLogOptions{EffortID: task, RunID: "malformed-resume-run", AgentID: "codex", Model: "unknown"}, workLogPublicationHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := activeWorkLogClaim(fixture.home, path); err != nil {
		t.Fatalf("intact claim prerequisite: %v", err)
	}
	originalClaim, err := os.ReadFile(outcome.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	projection := filepath.Join(path, workLogProjectionDirectory, workLogProjectionName)
	broken := []byte("broken requested projection\n")
	if err := os.WriteFile(projection, broken, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := locateResumableWorktree(context.Background(), canonical, fixture.home, task, "acme/app", path)
	if err == nil || got != "" || !strings.Contains(err.Error(), "read active work-log claim for registered worktree") {
		t.Fatalf("requested malformed projection = %q, %v", got, err)
	}
	if contents, err := os.ReadFile(projection); err != nil || !reflect.DeepEqual(contents, broken) {
		t.Fatalf("projection evidence changed = %q, %v", contents, err)
	}
	if contents, err := os.ReadFile(outcome.ClaimPath); err != nil || !reflect.DeepEqual(contents, originalClaim) {
		t.Fatalf("immutable claim changed = %q, %v", contents, err)
	}
	if got := gitTestOutput(t, path, "rev-parse", "HEAD"); got != head {
		t.Fatalf("refusal changed HEAD: %s", got)
	}
}

func TestE2ECanonicalPublicationCancelledNativeCommandHasDetailedRefusal(t *testing.T) {
	t.Parallel()
	repo := wtLifeCovNewRepo(t)
	canonical := &canonicalRepository{path: repo.path, root: repo.root, common: repo.common}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := gitCanonicalBytes(ctx, canonical, "status", "--porcelain=v1")
	if got != nil || err == nil || !strings.Contains(err.Error(), "canonical Git status --porcelain=v1: context canceled") {
		t.Fatalf("native cancelled helper=%q %v", got, err)
	}
	if head := gitTestOutput(t, repo.path, "rev-parse", "HEAD"); head != repo.head {
		t.Fatalf("cancelled command changed HEAD: %s", head)
	}
}

func TestE2ECanonicalPublicationDetachedRegistrationRefusesRecovery(t *testing.T) {
	t.Parallel()
	repo := wtLifeCovNewRepo(t)
	canonical := &canonicalRepository{path: repo.path, root: repo.root, common: repo.common}
	checkout := filepath.Join(t.TempDir(), "detached")
	gitTest(t, repo.path, "worktree", "add", "--detach", checkout, "main")
	got, err := registeredBranchNameCanonical(context.Background(), canonical, checkout)
	if got != "" || err == nil || !strings.Contains(err.Error(), "recover registered branch") || !strings.Contains(err.Error(), "has no branch") {
		t.Fatalf("native detached registration=%q %v", got, err)
	}
	if branch := gitTestOutput(t, checkout, "branch", "--show-current"); branch != "" {
		t.Fatalf("refusal attached branch=%q", branch)
	}
	if head := gitTestOutput(t, checkout, "rev-parse", "HEAD"); head != repo.head {
		t.Fatalf("refusal changed HEAD=%s", head)
	}
}

func TestE2ECanonicalPublicationQuarantineRequiresCurrentIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"source", "other"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	parent := wtLifeCovOpenDirectory(t, root)
	expected, err := secureDirectoryIdentityAt(int(parent.Fd()), "other")
	if err != nil {
		t.Fatal(err)
	}
	if err := quarantineStageDirectoryAt(parent, "missing", expected); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native missing stage=%v", err)
	}
	if err := quarantineStageDirectoryAt(parent, "source", expected); !errors.Is(err, errDirectoryMoveIdentityChanged) {
		t.Fatalf("other native inode admitted=%v", err)
	}
	for _, name := range []string{"source", "other"} {
		if info, err := os.Stat(filepath.Join(root, name)); err != nil || !info.IsDir() {
			t.Fatalf("refusal removed %s: %v %v", name, info, err)
		}
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 2 {
		t.Fatalf("identity refusal published extra evidence: %v %v", entries, err)
	}
}

func TestE2ECanonicalPublicationProjectsRootLoopRefusesAdmission(t *testing.T) {
	t.Parallel()
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Fatal(err)
	}
	if got, err := absoluteProjectsRoot(loop); got != "" || err == nil {
		t.Fatalf("native root loop=%q %v", got, err)
	}
	if target, err := os.Readlink(loop); err != nil || target != "loop" {
		t.Fatalf("root refusal replaced evidence: %q %v", target, err)
	}
}

func TestE2ECanonicalPublicationGuardRefusesInvalidBase(t *testing.T) {
	t.Parallel()
	repo := wtLifeCovNewRepo(t)
	got, err := Guard(context.Background(), repo.path, GuardOptions{ProjectsRoot: t.TempDir(), Base: "bad branch"})
	if !reflect.DeepEqual(got, GuardResult{}) || err == nil || !strings.Contains(err.Error(), "invalid base branch") {
		t.Fatalf("native base admission=%+v %v", got, err)
	}
	if head := gitTestOutput(t, repo.path, "rev-parse", "HEAD"); head != repo.head {
		t.Fatalf("invalid-base refusal changed HEAD=%s", head)
	}
}
