package orchestrate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/progress"
)

func TestPublishedRebatchPreservesEvidenceAcrossFastForwardTarget(t *testing.T) {
	for _, status := range []WorktreeMergeStatus{WorktreeMergeChecksFailed, WorktreeMergePublished} {
		t.Run(string(status), func(t *testing.T) {
			fixture := newEngineFixture(t)
			first := createMergeSource(t, fixture, "ff-first", "feature/ff-first", "first.txt", "first\n")
			extra := createMergeSource(t, fixture, "ff-extra", "feature/ff-extra", "extra.txt", "extra\n")
			old, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
			if err != nil {
				t.Fatal(err)
			}
			runEngineGit(t, old.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+old.Candidate.Branch)
			old.Phase, old.Status, old.PullRequest, old.PublishedCandidateSHA = WorktreeMergePhaseLand, status, "41", old.Candidate.SHA
			if err := persistWorktreeMergeReceipt(old); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(old.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_CANDIDATE_SHA", old.Candidate.SHA)
			writeEngineFile(t, filepath.Join(fixture.canonical, "target-advance.txt"), "target advance\n")
			runEngineGit(t, fixture.canonical, "add", "target-advance.txt")
			runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance target")
			runEngineGit(t, fixture.canonical, "push", "origin", "main")
			advanced := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			replacement, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir, extra.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: old.ReceiptPath})
			if err != nil {
				t.Fatal(err)
			}
			if replacement.TargetSHA != advanced {
				t.Fatalf("target = %s, want %s", replacement.TargetSHA, advanced)
			}
			for _, ancestor := range []string{advanced, old.Candidate.SHA} {
				contains, err := isMergeAncestor(context.Background(), replacement.Candidate.Worktree, ancestor, replacement.Candidate.SHA)
				if err != nil || !contains {
					t.Fatalf("replacement lacks ancestor %s: %v", ancestor, err)
				}
			}
			after, err := os.ReadFile(old.ReceiptPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("historical receipt changed: %v", err)
			}
			ack, err := readPreparedWorktreeMergeRebatch(rebatchPath(old.ReceiptPath), old)
			if err != nil || ack.ReceiptTargetSHA != old.TargetSHA || ack.CurrentTargetSHA != advanced {
				t.Fatalf("target evidence = %+v, %v", ack, err)
			}
			active, err := activeWorktreeMergeLaneReceipt(context.Background(), fixture.githubDir, filepath.Dir(old.ReceiptPath), old.Lane)
			if err != nil || active == nil || active.ReceiptPath != replacement.ReceiptPath {
				t.Fatalf("active replacement = %+v, %v", active, err)
			}
			if _, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: old.ReceiptPath}); err == nil || !strings.Contains(err.Error(), "rebatched") {
				t.Fatalf("old landing replay = %v", err)
			}
			// Even self-consistent corrupted acknowledgement/receipt target fields
			// cannot authorize cleanup without replaying the ancestry proof.
			unrelated := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", "HEAD^{tree}", "-m", "test: unrelated cleanup target"))
			replacement.TargetSHA, replacement.LandingSHA = unrelated, replacement.Candidate.SHA
			if err := persistWorktreeMergeReceipt(replacement); err != nil {
				t.Fatal(err)
			}
			ack.CurrentTargetSHA = unrelated
			ack.ID = preparedRebatchID(ack)
			if err := persistPreparedWorktreeMergeRebatch(rebatchPath(old.ReceiptPath), ack); err != nil {
				t.Fatal(err)
			}
			if err := validateRebatchedWorktreeMergeCleanup(context.Background(), fixture.githubDir, replacement); err == nil || !strings.Contains(err.Error(), "not a proven fast-forward") {
				t.Fatalf("corrupted cleanup proof = %v", err)
			}
		})
	}
}

func TestPublishedRebatchTargetRaceRetainsDurableCandidateReceipt(t *testing.T) {
	fixture := newEngineFixture(t)
	first := createMergeSource(t, fixture, "race-first", "feature/race-first", "first.txt", "first\n")
	extra := createMergeSource(t, fixture, "race-extra", "feature/race-extra", "extra.txt", "extra\n")
	old, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, old.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+old.Candidate.Branch)
	old.Phase, old.Status, old.PullRequest, old.PublishedCandidateSHA = WorktreeMergePhaseLand, WorktreeMergePublished, "41", old.Candidate.SHA
	if err := persistWorktreeMergeReceipt(old); err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", old.Candidate.SHA)
	advanced := ""
	failed, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir, extra.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: old.ReceiptPath,
		Progress: func(event progress.Event) {
			if event.Phase != "create_candidate" || event.State != progress.Started {
				return
			}
			writeEngineFile(t, filepath.Join(fixture.canonical, "race.txt"), "target race\n")
			runEngineGit(t, fixture.canonical, "add", "race.txt")
			runEngineGit(t, fixture.canonical, "commit", "-m", "test: target advances during candidate creation")
			runEngineGit(t, fixture.canonical, "push", "origin", "main")
			advanced = strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
		},
	})
	if err == nil || !strings.Contains(err.Error(), "target changed during candidate creation") {
		t.Fatalf("race refusal = %v", err)
	}
	if advanced == "" || failed.Status != WorktreeMergeConflict || failed.Candidate.SHA != advanced || failed.TargetSHA != advanced || failed.RebatchOf != old.ReceiptPath {
		t.Fatalf("missing durable candidate identity: %+v", failed)
	}
	persisted, err := readWorktreeMergeReceipt(failed.ReceiptPath)
	if err != nil || persisted.Candidate != failed.Candidate || persisted.Status != WorktreeMergeConflict {
		t.Fatalf("failed receipt = %+v, %v", persisted, err)
	}
	if _, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, persisted, persisted.Candidate); err != nil {
		t.Fatalf("failed candidate lost its exact worktree/ref/claim: %v", err)
	}
	if _, err := os.Stat(rebatchPath(old.ReceiptPath)); !os.IsNotExist(err) {
		t.Fatalf("race falsely superseded old receipt: %v", err)
	}
}

func TestPublishedRebatchRefusesRewoundOrLandedTarget(t *testing.T) {
	for _, mode := range []string{"rewound", "landed", "no additional source"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newEngineFixture(t)
			ancestor := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			writeEngineFile(t, filepath.Join(fixture.canonical, "baseline.txt"), "baseline\n")
			runEngineGit(t, fixture.canonical, "add", "baseline.txt")
			runEngineGit(t, fixture.canonical, "commit", "-m", "test: baseline before publication")
			runEngineGit(t, fixture.canonical, "push", "origin", "main")
			first := createMergeSource(t, fixture, "refusal-first", "feature/refusal-first", "first.txt", "first\n")
			extra := createMergeSource(t, fixture, "refusal-extra", "feature/refusal-extra", "extra.txt", "extra\n")
			old, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
			if err != nil {
				t.Fatal(err)
			}
			runEngineGit(t, old.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+old.Candidate.Branch)
			old.Phase, old.Status, old.PullRequest, old.PublishedCandidateSHA = WorktreeMergePhaseLand, WorktreeMergePublished, "41", old.Candidate.SHA
			if err := persistWorktreeMergeReceipt(old); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(old.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_CANDIDATE_SHA", old.Candidate.SHA)
			sources := []string{first.WorktreeDir, extra.WorktreeDir}
			want := "not a proven fast-forward"
			switch mode {
			case "rewound":
				runEngineGit(t, fixture.canonical, "push", "--force", "origin", ancestor+":refs/heads/main")
			case "landed":
				runEngineGit(t, fixture.canonical, "push", "origin", old.Candidate.SHA+":refs/heads/main")
				want = "must remain unlanded"
			default:
				writeEngineFile(t, filepath.Join(fixture.canonical, "advance.txt"), "advance\n")
				runEngineGit(t, fixture.canonical, "add", "advance.txt")
				runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance target without another source")
				runEngineGit(t, fixture.canonical, "push", "origin", "main")
				sources = sources[:1]
				want = "must add at least one distinct source"
			}
			_, err = PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: sources, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: old.ReceiptPath})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal = %v, want %s", err, want)
			}
			after, err := os.ReadFile(old.ReceiptPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("refusal changed historical receipt: %v", err)
			}
			if _, err := os.Stat(rebatchPath(old.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("refusal created acknowledgement: %v", err)
			}
		})
	}
}
