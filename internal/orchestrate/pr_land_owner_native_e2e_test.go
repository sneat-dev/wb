//go:build e2e

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// nativeLandOwnerProtocol shares the immutable engine seed/explicit root and
// observes every custody/ancestry SHA from actual Git. Hosted checks are fixture
// protocol responses; successful rewrite, proof resolution and target adoption
// run their native production owners, with no process-wide fixture state.
type nativeLandOwner struct {
	*landOwnerProtocol
	nativeDirectories []string
	nativeRemote      string
}

func nativeLandOwnerProtocol(t *testing.T) (engineFixture, worktrees.CreateResult, *nativeLandOwner) {
	t.Helper()
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "land-owner-source", "feature/source", "first.txt", "first\n")
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "second.txt"), "second\n")
	runEngineGit(t, source.WorktreeDir, "add", "second.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "second source change")
	runEngineGit(t, source.WorktreeDir, "push", "origin", source.Branch)
	p := &nativeLandOwner{landOwnerProtocol: newLandOwnerProtocol(t)}
	p.nativeDirectories = []string{fixture.canonical, source.WorktreeDir}
	p.nativeRemote = fixture.repository.CloneURL
	p.head = strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+source.Branch))
	p.target = strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main"))
	commits := orchCovSourceCommits(t, source.WorktreeDir, p.target, p.head)
	wire := make([]map[string]any, 0, len(commits))
	for _, commit := range commits {
		wire = append(wire, map[string]any{"sha": commit.SHA, "commit": map[string]string{"message": commit.Subject}})
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	p.commits = raw
	p.parentBody = func(sha string) []byte {
		parents := strings.Fields(runEngineGit(t, fixture.repository.CloneURL, "show", "-s", "--format=%P", sha))
		entries := make([]map[string]string, 0, len(parents))
		for _, parent := range parents {
			entries = append(entries, map[string]string{"sha": parent})
		}
		raw, err := json.Marshal(map[string]any{"parents": entries})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	p.compareBody = func(comparison string) []byte {
		parts := strings.Split(comparison, "...")
		if len(parts) != 2 {
			t.Fatalf("comparison %q", comparison)
		}
		base := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", parts[0]))
		head := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", parts[1]))
		mergeBase := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "merge-base", base, head))
		status := "diverged"
		if base == head {
			status = "identical"
		} else if base == mergeBase {
			status = "ahead"
		} else if head == mergeBase {
			status = "behind"
		}
		return []byte(fmt.Sprintf(`{"status":%q,"base_commit":{"sha":%q},"merge_base_commit":{"sha":%q}}`, status, base, mergeBase))
	}
	return fixture, source, p
}

func nativeLandOwnerContext(t *testing.T, p *nativeLandOwner) context.Context {
	t.Helper()
	return githubobserver.WithReader(context.Background(), githubobserver.Reader{
		Get: p.get,
		Read: func(_ context.Context, dir string, args ...string) ([]byte, error) {
			t.Fatalf("unexpected native hosted Read %s %q", dir, args)
			return nil, errors.New("unexpected Read")
		},
		Execute: func(ctx context.Context, dir string, args ...string) githubobserver.CommandResponse {
			if len(args) == 3 && args[0] == "api" && args[1] == "--paginate" && strings.HasPrefix(args[2], "repos/acme/app/commits/") && strings.HasSuffix(args[2], "/pulls") {
				found := false
				for _, owned := range p.nativeDirectories {
					if dir == owned {
						found = true
					}
				}
				if !found {
					t.Fatalf("inventory queried outside owned checkout %s %q", dir, args)
				}
				sha := strings.TrimSuffix(strings.TrimPrefix(args[2], "repos/acme/app/commits/"), "/pulls")
				runEngineGit(t, p.nativeRemote, "cat-file", "-e", sha+"^{commit}")
				return githubobserver.CommandResponse{Stdout: []byte(`[]`)}
			}
			if len(args) == 2 && args[0] == "api" && args[1] == "repos/acme/app/git/ref/heads/"+p.branch {
				if dir != "" {
					t.Fatalf("branch verification cwd %s", dir)
				}
				actual := strings.TrimSpace(runEngineGit(t, p.nativeRemote, "for-each-ref", "--format=%(objectname)", "refs/heads/"+p.branch))
				if actual == "" {
					return githubobserver.CommandResponse{ExitCode: 1, Err: errors.New("actual branch absent"), Stderr: []byte("Not Found")}
				}
				return githubobserver.CommandResponse{Stdout: []byte(fmt.Sprintf(`{"object":{"sha":%q}}`, actual))}
			}
			if len(args) > 3 && args[0] == "api" && args[1] == "graphql" && strings.Contains(args[3], "closingIssuesReferences") {
				query := "query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){closingIssuesReferences(first:50){nodes{number}}}}}"
				want := []string{"api", "graphql", "-f", "query=" + query, "-f", "owner=acme", "-f", "name=app", "-F", "number=7"}
				if dir != "" || !reflect.DeepEqual(args, want) {
					t.Fatalf("closing issues query %s %q", dir, args)
				}
				return githubobserver.CommandResponse{Stdout: []byte(`{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":[]}}}}}`)}
			}
			return p.execute(ctx, dir, args...)
		},
	})
}

type landOwnerFetchFailure struct {
	Git
	t                 *testing.T
	directory, branch string
	cause             error
	calls             int
}

func (g *landOwnerFetchFailure) FetchRefs(_ context.Context, dir, remote string, refs ...string) error {
	g.calls++
	if dir != g.directory || remote != "origin" || !reflect.DeepEqual(refs, []string{"main", g.branch}) {
		g.t.Fatalf("keep fetch identity %s %s %v", dir, remote, refs)
	}
	return g.cause
}

func TestE2ELandKeptRewritePreservesNativePublicationOnFailure(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"keep fetch failure", "rewritten budget exhausted"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			fixture, source, p := nativeLandOwnerProtocol(t)
			original := p.head
			originalTarget := p.target
			options := landOptions(&landFixture{projects: fixture.githubDir})
			options.NoAutoMerge = true
			options.NoUpdateBranch = true
			options.MergeMethod = "squash"
			options.MergeMethodExplicit = true
			commits := orchCovSourceCommits(t, source.WorktreeDir, p.target, p.head)
			if len(commits) != 2 {
				t.Fatalf("native source commits %+v", commits)
			}
			options.KeepCommits = []string{commits[0].SHA}
			options.Reason = "keep independently bisectable source"
			options.BuildCommand = []string{"sh", "-c", "exit 0"}
			sentinel := errors.New("owned native " + kind)
			var failedFetch *landOwnerFetchFailure
			if kind == "keep fetch failure" {
				failedFetch = &landOwnerFetchFailure{Git: defaultGit, t: t, directory: fixture.canonical, branch: source.Branch, cause: sentinel}
				options.git = failedFetch
			}
			publishedClockReads := 0
			if kind == "rewritten budget exhausted" {
				// Preserve the existing fixture's full ten-second budget and poll
				// cadence. Once native rewrite has actually published, the first
				// clock read establishes its normal deadline; the second observes
				// elapsed time beyond that whole budget. No timeout is shortened.
				clockStart := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
				fullBudget := options.Slice
				if fullBudget != 10*time.Second {
					t.Fatalf("native fixture changed foreground budget: %s", fullBudget)
				}
				options.Now = func() time.Time {
					actual := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+source.Branch))
					if actual == original {
						return clockStart
					}
					publishedClockReads++
					if publishedClockReads == 1 {
						return clockStart
					}
					return clockStart.Add(fullBudget + time.Millisecond)
				}
			}
			p.beforeGet = func(githubobserver.GetRequest) {
				p.head = strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+source.Branch))
			}
			result, err := landPullRequest(nativeLandOwnerContext(t, p), options)
			if err == nil || p.merges != 0 || p.arms != 0 || result.BranchDeleted || len(result.CleanedTasks) > 0 {
				t.Fatalf("native keep refusal %+v err %v calls %v", result, err, p.calls)
			}
			published := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+source.Branch))
			if kind == "keep fetch failure" {
				if !errors.Is(err, sentinel) || failedFetch.calls != 1 || published != original || result.HeadSHA != original || !strings.Contains(err.Error(), "fetch the branch before rewriting it") {
					t.Fatalf("pre-rewrite refusal %+v err %v fetches %d published %s", result, err, failedFetch.calls, published)
				}
			} else {
				if publishedClockReads != 2 || err.Error() != "pull request landing timeout must be positive" || published == original || result.HeadSHA != published || result.Evidence["rewritten_head"] != shortMergeRevision(published) || len(result.Commits) != 2 || len(result.KeptCommits) != 1 || result.KeptCommits[0] != commits[0].SHA {
					t.Fatalf("rewritten publication was lost %+v, err %v published %s post-publication clock reads %d", result, err, published, publishedClockReads)
				}
				parents := strings.Fields(runEngineGit(t, fixture.repository.CloneURL, "rev-list", "--reverse", originalTarget+".."+published))
				if len(parents) != 2 {
					t.Fatalf("actual rewritten DAG %v", parents)
				}
			}
			if sourceHead := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); sourceHead != original {
				t.Fatalf("kept rewrite moved author checkout %s", sourceHead)
			}
			if target := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); target != originalTarget {
				t.Fatalf("failed landing moved target %s", target)
			}
		})
	}
}

func TestE2ELandPostWaitReviewRefusalUsesRestoredNativeForeignHistory(t *testing.T) {
	t.Parallel()
	fixture, source, p := nativeLandOwnerProtocol(t)
	reviewed := p.target
	head := p.head
	// Current head has exactly one parent in this real DAG; it cannot be a
	// WB update-branch merge. The first phase has no proof checkout available.
	parents := strings.Fields(runEngineGit(t, fixture.repository.CloneURL, "show", "-s", "--format=%P", head))
	if len(parents) != 1 {
		t.Fatalf("native foreign head parents %v", parents)
	}
	review := filepath.Join(t.TempDir(), "review.txt")
	if err := os.WriteFile(review, []byte("Reviewed-Head: "+reviewed+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// An explicit separate projects root has no proof checkout at admission.
	// All candidate Git assets remain under the original test root.
	proofProjects := filepath.Join(t.TempDir(), "projects")
	proofPath := filepath.Join(proofProjects, "acme", "app")
	if err := os.MkdirAll(filepath.Dir(proofPath), 0700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(t.TempDir(), "staged-canonical")
	runEngineGit(t, fixture.githubDir, "clone", "--no-hardlinks", fixture.repository.CloneURL, staged)
	t.Cleanup(func() {
		if _, err := os.Stat(proofPath); err == nil {
			_ = os.Rename(proofPath, staged)
		}
	})
	restored := false
	p.beforeGet = func(req githubobserver.GetRequest) {
		if req.Endpoint == "repos/acme/app/pulls/7/commits?per_page=100" {
			if err := os.Rename(staged, proofPath); err != nil {
				t.Fatal(err)
			}
			restored = true
		}
	}
	options := landOptions(&landFixture{projects: proofProjects})
	options.NoAutoMerge = true
	options.NoUpdateBranch = true
	options.ApprovedBy = review
	p.files = []byte(`[{"filename":"first.txt","status":"modified","patch":"@@ -1 +1 @@\n-old\n+first\n"}]`)
	result, err := landPullRequest(nativeLandOwnerContext(t, p), options)
	if err != nil || result.Outcome != LandRefused || result.RefusalCode != LandRefusalReviewStale || !restored || p.merges != 0 || p.arms != 0 || result.AutoMergeArmed || result.HeadSHA != head || !strings.Contains(result.Evidence["review"], "review-unverified:") {
		t.Fatalf("postwait native review refusal %+v err %v reads %v", result, err, p.calls)
	}
	if !strings.Contains(strings.Join(p.calls, "\n"), "repos/acme/app/commits/"+head) {
		t.Fatalf("postwait never read real foreign DAG parents %v", p.calls)
	}
	if current := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); current != head {
		t.Fatalf("proof refused but source moved %s", current)
	}
	if remoteTarget := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remoteTarget != reviewed {
		t.Fatalf("proof refusal moved target %s", remoteTarget)
	}
}

func TestE2ELandGitHubAutoMergeReviewBindingAdoptsNativeTarget(t *testing.T) {
	t.Parallel()
	fixture, source, p := nativeLandOwnerProtocol(t)
	originalHead := p.head
	review := filepath.Join(t.TempDir(), "review.txt")
	if err := os.WriteFile(review, []byte("Reviewed-Head: "+originalHead+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p.files = []byte(`[{"filename":"first.txt","status":"modified","patch":"@@ -1 +1 @@\n-old\n+first\n"}]`)
	p.mutation = func(args []string) githubobserver.CommandResponse {
		if len(args) < 2 || args[0] != "api" || args[1] != "graphql" {
			t.Fatalf("GitHub won race but WB attempted write %q", args)
		}
		values := strings.Join(args, "\x00")
		for _, bound := range []string{"expectedHeadOid:$head", "id=PR_owned", "head=" + originalHead, "method=MERGE"} {
			if !strings.Contains(values, bound) {
				t.Fatalf("arming lost binding %s argv %q", bound, args)
			}
		}
		return githubobserver.CommandResponse{Stdout: []byte(`{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"autoMergeRequest":{"enabledAt":"2026-10-06T00:00:00Z"}}}}}`)}
	}
	merged := false
	p.beforeGet = func(req githubobserver.GetRequest) {
		if !merged && strings.HasSuffix(req.Endpoint, "/check-runs?per_page=100") && p.checkReads >= 1 {
			// A hosted actor wins the actual target lease between the two stable
			// checks observations. Native finalization must prove this exact landing.
			runEngineGit(t, fixture.repository.CloneURL, "update-ref", "refs/heads/main", originalHead, p.target)
			p.target = originalHead
			p.merged = true
			merged = true
		}
	}
	options := landOptions(&landFixture{projects: fixture.githubDir})
	options.NoUpdateBranch = true
	options.ApprovedBy = review
	result, err := landPullRequest(nativeLandOwnerContext(t, p), options)
	if err != nil || result.Outcome != LandSuccess || !merged || !result.AutoMergeArmed || p.arms != 1 || p.merges != 0 || result.Evidence["merged_by"] != "github auto-merge" || result.ReviewBound == nil || !*result.ReviewBound || result.ReviewedHeadSHA != originalHead {
		t.Fatalf("native auto-merge review adoption %+v err %v calls %v", result, err, p.calls)
	}
	if target := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); target != originalHead {
		t.Fatalf("adopted target mismatch %s", target)
	}
	if current := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); current != originalHead {
		t.Fatalf("adoption moved kept source %s", current)
	}
}
