//go:build e2e

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2EPRFinalizationCleanupFailureKeepsDurableCustody(t *testing.T) {
	t.Parallel()
	fixture, source, p := nativeLandOwnerProtocol(t)
	unrelated := createMergeSource(t, fixture, "unrelated-finalization", "feature/unrelated", "unrelated.txt", "unrelated source\n")
	runEngineGit(t, unrelated.WorktreeDir, "push", "origin", unrelated.Branch)
	p.nativeDirectories = append(p.nativeDirectories, unrelated.WorktreeDir)
	unrelatedHead := strings.TrimSpace(runEngineGit(t, unrelated.WorktreeDir, "rev-parse", "HEAD"))
	unrelatedClaim, err := os.ReadFile(unrelated.WorkLogPath)
	if err != nil {
		t.Fatal(err)
	}
	head := p.head
	claim, err := os.ReadFile(source.WorkLogPath)
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", head)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	p.target = head
	ctx := nativeLandOwnerContext(t, p)
	home, err := wbhome.Root(fixture.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(home, "reports", "worktree-cleanup")
	if err := os.MkdirAll(blocker, 0700); err != nil {
		t.Fatal(err)
	}
	// Keep the backlog parent readable; only publication of the planned report
	// into its new timestamp directory is denied by the owned filesystem.
	t.Cleanup(func() { _ = os.Chmod(blocker, 0700) })
	if err := os.Chmod(blocker, 0500); err != nil {
		t.Fatal(err)
	}
	cleaned, reports, err := cleanupLandedWorktrees(ctx, fixture.githubDir, "acme/app", source.Branch, head, "main", head)
	var pathErr *os.PathError
	if len(cleaned) != 0 || len(reports) != 0 || !errors.As(err, &pathErr) || pathErr.Err == nil || !strings.Contains(err.Error(), "retire worktree for task land-owner-source") || !strings.Contains(err.Error(), "create cleanup report directory") || pathErr.Op != "mkdir" || filepath.Dir(pathErr.Path) != blocker {
		t.Fatalf("planned report failure cleaned=%v reports=%v err=%v", cleaned, reports, err)
	}
	if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); got != head {
		t.Fatalf("report failure moved source %s", got)
	}
	after, readErr := os.ReadFile(source.WorkLogPath)
	if readErr != nil || !reflect.DeepEqual(after, claim) {
		t.Fatalf("report failure changed claim %v", readErr)
	}
	if got, statErr := os.Stat(blocker); statErr != nil || !got.IsDir() || got.Mode().Perm() != 0500 {
		t.Fatalf("report obstruction mutated %v %v", got, statErr)
	}
	if err := os.Chmod(blocker, 0700); err != nil {
		t.Fatal(err)
	}
	dirty := filepath.Join(source.WorktreeDir, "owned-uncommitted.txt")
	if err := os.WriteFile(dirty, []byte("retained dirty custody"), 0600); err != nil {
		t.Fatal(err)
	}
	cleaned, reports, err = cleanupLandedWorktrees(ctx, fixture.githubDir, "acme/app", source.Branch, head, "main", head)
	if len(cleaned) != 0 || len(reports) != 1 || err == nil || !strings.Contains(err.Error(), "worktree for task land-owner-source was not retired") {
		t.Fatalf("dirty custody refusal cleaned=%v reports=%v err=%v", cleaned, reports, err)
	}
	if after, readErr := os.ReadFile(source.WorkLogPath); readErr != nil || !reflect.DeepEqual(after, claim) {
		t.Fatalf("dirty refusal changed claim %v", readErr)
	}
	if got, readErr := os.ReadFile(dirty); readErr != nil || string(got) != "retained dirty custody" {
		t.Fatalf("dirty custody changed %q %v", got, readErr)
	}
	if err := os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	cleaned, reports, err = cleanupLandedWorktrees(ctx, fixture.githubDir, "acme/app", source.Branch, head, "main", head)
	if err != nil || !reflect.DeepEqual(cleaned, []string{"land-owner-source"}) || len(reports) != 1 {
		t.Fatalf("cleanup retry cleaned=%v reports=%v err=%v", cleaned, reports, err)
	}
	report, err := os.ReadFile(reports[0])
	if err != nil {
		t.Fatal(err)
	}
	var durable struct {
		Phase   string `json:"phase"`
		Results []struct {
			Applied     bool   `json:"applied"`
			Repository  string `json:"repository"`
			WorktreeDir string `json:"worktree_dir"`
		} `json:"results"`
	}
	if err := json.Unmarshal(report, &durable); err != nil || durable.Phase != "applied" || len(durable.Results) != 1 || !durable.Results[0].Applied || durable.Results[0].Repository != "acme/app" || durable.Results[0].WorktreeDir != source.WorktreeDir {
		t.Fatalf("durable cleanup report %s error=%v", report, err)
	}
	if err := worktrees.ValidateRemovedTerminalWorkLogs(fixture.githubDir, []worktrees.TerminalWorkLogExpectation{{Task: "land-owner-source", Repository: "acme/app", Worktree: source.WorktreeDir, Branch: source.Branch, Base: "main", FinalCommit: head}}); err != nil {
		t.Fatalf("native immutable terminal proof %v", err)
	}
	if _, err := os.Stat(source.WorktreeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired source path %v", err)
	}
	if got := runEngineGit(t, fixture.canonical, "worktree", "list", "--porcelain"); strings.Contains(got, source.WorktreeDir) || !strings.Contains(got, unrelated.WorktreeDir) {
		t.Fatalf("native registration after cleanup %s", got)
	}
	if got := strings.TrimSpace(runEngineGit(t, unrelated.WorktreeDir, "rev-parse", "HEAD")); got != unrelatedHead {
		t.Fatalf("unrelated source moved %s", got)
	}
	after, err = os.ReadFile(unrelated.WorkLogPath)
	if err != nil || !reflect.DeepEqual(after, unrelatedClaim) {
		t.Fatalf("unrelated claim changed %v", err)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+unrelated.Branch)); got != unrelatedHead {
		t.Fatalf("unrelated remote moved %s", got)
	}
	for _, dir := range []string{fixture.canonical, fixture.repository.CloneURL} {
		if got := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "main")); got != head {
			t.Fatalf("landed target moved %s %s", dir, got)
		}
	}
}

func TestE2EPRFinalizationUpdatedHeadHookRejectsActualNativeAdvance(t *testing.T) {
	t.Parallel()
	fixture, source, p := nativeLandOwnerProtocol(t)
	original := p.head
	claim, err := os.ReadFile(source.WorkLogPath)
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(fixture.canonical, "advanced-target.txt"), "native target advance\n")
	runEngineGit(t, fixture.canonical, "add", "advanced-target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "advance native target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	p.target = target
	view, err := githubchecks.ReadPullRequest(p.context(), "acme/app", "7")
	if err != nil {
		t.Fatal(err)
	}
	p.calls = nil
	mutations, hooks := 0, 0
	cause := errors.New("owned updated-head custody hook refused")
	options := PullRequestLandOptions{Repository: "acme/app", NoAutoMerge: true, Slice: 10 * time.Second, CheckPollInterval: time.Millisecond, headUpdated: func(previous, updated string) error {
		hooks++
		actual := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+source.Branch))
		if previous != original || updated != actual || updated != p.head || updated == original {
			t.Fatalf("native hook identity %s %s actual=%s", previous, updated, actual)
		}
		runEngineGit(t, source.WorktreeDir, "merge-base", "--is-ancestor", target, updated)
		return cause
	}}
	ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Get: p.get, Read: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("unexpected hosted Read")
		return nil, cause
	}, Execute: func(_ context.Context, dir string, args ...string) githubobserver.CommandResponse {
		mutations++
		want := []string{"api", "--method", "PUT", "repos/acme/app/pulls/7/update-branch", "-f", "expected_head_sha=" + original}
		if dir != "" || !reflect.DeepEqual(args, want) {
			t.Fatalf("native update CAS %q %v", dir, args)
		}
		runEngineGit(t, source.WorktreeDir, "fetch", "origin", "main")
		runEngineGit(t, source.WorktreeDir, "merge", "--no-edit", "origin/main")
		runEngineGit(t, source.WorktreeDir, "push", "origin", source.Branch)
		p.head = strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+source.Branch))
		return githubobserver.CommandResponse{Stdout: []byte(`{"message":"Updating pull request branch."}`)}
	}})
	evidence := map[string]string{"retained": "owned"}
	updated, waited, armed, merged, refusal, err := awaitLandablePullRequest(ctx, options, view, "7", "subject", "body", evidence)
	if !errors.Is(err, cause) || refusal != nil || armed || merged || mutations != 1 || hooks != 1 || updated.Head.SHA != p.head || waited.Status != "" || p.checkReads != 0 || evidence["updated_onto_target"] != shortMergeRevision(p.head) || evidence["head"] != shortMergeRevision(p.head) || evidence["retained"] != "owned" || evidence["local_sync"] != "" {
		t.Fatalf("native hook receipt %+v waited=%+v err=%v mutations=%d hooks=%d evidence=%v", updated, waited, err, mutations, hooks, evidence)
	}
	wantReads := []string{"repos/acme/app/git/ref/heads/main", "repos/acme/app/compare/" + target + "..." + original, "repos/acme/app/pulls/7", "repos/acme/app/pulls/7"}
	if !reflect.DeepEqual(p.calls, wantReads) {
		t.Fatalf("native update observations %v", p.calls)
	}
	after, err := os.ReadFile(source.WorkLogPath)
	if err != nil || !reflect.DeepEqual(after, claim) {
		t.Fatalf("hook failure changed claim %v", err)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != target {
		t.Fatalf("hook moved canonical %s", got)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); got != target {
		t.Fatalf("hook moved target %s", got)
	}
	if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); got != p.head {
		t.Fatalf("published source mismatch %s", got)
	}
}
