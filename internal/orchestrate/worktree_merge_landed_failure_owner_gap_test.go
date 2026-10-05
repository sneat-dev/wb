package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLandedFailureOwnerNativeSiblingAndStatBoundaryRefusals(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"candidate stat", "receipt target", "source containment", "landing read", "landing containment"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f, r := landedFailureOwnerFixture(t)
			before, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			changed := r
			changed.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			tree := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", r.Candidate.SHA+"^{tree}"))
			sibling := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "commit-tree", tree, "-p", r.TargetSHA, "-m", "test: real sibling outside candidate ancestry"))
			if yes, e := isMergeAncestor(t.Context(), r.Candidate.Worktree, sibling, r.Candidate.SHA); e != nil || yes {
				t.Fatalf("native sibling containment=%t %v", yes, e)
			}
			want := ""
			run := defaultRunner
			var selected *landedFailureOwnerRunner
			if stage == "candidate stat" || stage == "landing read" || stage == "landing containment" {
				changed.Status = WorktreeMergePostTargetCIFailed
				changed.Phase = WorktreeMergePhaseLand
				changed.LandingSHA = sibling
				changed.Checks.Status = PullRequestWaitFailed
				changed.Checks.Head = sibling
			}
			switch stage {
			case "candidate stat":
				blocker := filepath.Join(t.TempDir(), "blocker")
				if e := os.WriteFile(blocker, []byte("file"), 0600); e != nil {
					t.Fatal(e)
				}
				changed.Candidate.Worktree = filepath.Join(blocker, "candidate")
				want = "inspect receipted candidate worktree"
			case "receipt target":
				changed.TargetSHA = sibling
				want = "does not contain immutable receipt target"
			case "source containment":
				changed.Sources[0].SHA = sibling
				runEngineGit(t, r.Sources[0].Worktree, "reset", "--hard", sibling)
				want = "does not contain receipted source"
			case "landing read":
				sentinel := errors.New("selected receipted landing ancestry")
				selected = &landedFailureOwnerRunner{Runner: defaultRunner, path: r.Candidate.Worktree, args: []string{"merge-base", sibling, r.Candidate.SHA}, ordinal: 1, sentinel: sentinel}
				run = selected
				want = sentinel.Error()
			case "landing containment":
				want = "does not contain receipted landing"
			}
			// Actual receipt read remains native; negative input alters only the selected evidence field.
			read := func(path string) (WorktreeMergeReceipt, error) {
				native, e := readWorktreeMergeReceipt(path)
				if e != nil {
					return native, e
				}
				return changed, nil
			}
			_, e := acknowledgeLandedMergeFailure(t.Context(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath}, run, read, persistLandedFailureAcknowledgement)
			if e == nil || !strings.Contains(e.Error(), want) {
				t.Fatalf("stage=%s error=%v want=%q", stage, e, want)
			}
			if selected != nil && (!errors.Is(e, selected.sentinel) || selected.seen != 1) {
				t.Fatalf("landing query cause=%v consumed=%d", e, selected.seen)
			}
			if stage == "candidate stat" {
				var pathError *os.PathError
				if !errors.As(e, &pathError) || pathError.Path != changed.Candidate.Worktree {
					t.Fatalf("native stat identity=%v", e)
				}
			}
			after, e := os.ReadFile(r.ReceiptPath)
			if e != nil || string(after) != string(before) {
				t.Fatalf("historical receipt changed: %v", e)
			}
			if _, e := os.Stat(landedFailureAcknowledgementPath(r.ReceiptPath)); !os.IsNotExist(e) {
				t.Fatalf("refusal sidecar=%v", e)
			}
		})
	}
}

//nolint:paralleltest // Real Land/cleanup uses a process-wide GH/PATH provider; reversible rows share its private native terminal evidence.
func TestLandedFailureCleanedBoundaryRefusalsPreserveTerminalEvidence(t *testing.T) {
	f, _, r, _ := landedTerminalCleanupFixture(t)
	r.Status = WorktreeMergePostTargetCIFailed
	r.Checks.Status = PullRequestWaitFailed
	r.Checks.Head = r.LandingSHA
	if e := persistWorktreeMergeReceipt(r); e != nil {
		t.Fatal(e)
	}
	externallyTerminalizeMergeCleanup(t, f, &r)
	o := WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath}
	if _, e := AcknowledgeLandedMergeFailure(t.Context(), o); e != nil {
		t.Fatalf("genuine terminal baseline=%v", e)
	}
	before, e := os.ReadFile(r.ReceiptPath)
	if e != nil {
		t.Fatal(e)
	}
	for _, stage := range []string{"identity", "canonical coordinate", "checkout stat", "report head", "internal ancestry error", "internal ancestry false"} {
		//nolint:paralleltest // Rows retain the same native cleanup proof files and execute without overlapping mutations.
		t.Run(stage, func(t *testing.T) {
			changed := r
			changed.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			options := o
			want := ""
			run := defaultRunner
			var selected *landedFailureOwnerRunner
			switch stage {
			case "identity":
				changed.Candidate.Branch = ""
				want = "lacks exact candidate identity"
			case "canonical coordinate":
				changed.Repository = "invalid"
				want = "resolve canonical repository for cleaned landing"
			case "checkout stat":
				blocker := filepath.Join(t.TempDir(), "blocker")
				if e := os.WriteFile(blocker, []byte("file"), 0600); e != nil {
					t.Fatal(e)
				}
				changed.Candidate.Worktree = filepath.Join(blocker, "candidate")
				want = "inspect receipted checkout"
			case "report head":
				changed.Sources[0].SHA = r.TargetSHA
				want = "terminal cleanup receipt does not exactly match"
			case "internal ancestry error":
				sentinel := errors.New("selected internal cleaned ancestry")
				selected = &landedFailureOwnerRunner{Runner: defaultRunner, path: f.canonical, args: []string{"merge-base", r.TargetSHA, r.Candidate.SHA}, ordinal: 2, sentinel: sentinel}
				run = selected
				want = "verify candidate ancestry"
			case "internal ancestry false":
				tree := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", r.Candidate.SHA+"^{tree}"))
				sibling := strings.TrimSpace(runEngineGit(t, f.canonical, "commit-tree", tree, "-p", r.TargetSHA, "-m", "test: cleaned native sibling"))
				// Direct ancestry validator negative input makes no claim of valid terminal custody.
				bases := map[string]string{r.Candidate.Task: sibling, r.Sources[0].Task: r.TargetSHA}
				err := validateCleanedLandedFailureAncestry(t.Context(), defaultRunner, f.canonical, r, bases)
				if err == nil || !strings.Contains(err.Error(), "does not contain immutable candidate claim base") {
					t.Fatalf("native connected false=%v", err)
				}
				return
			}
			_, err := acknowledgeCleanedDirectPostTargetFailure(t.Context(), options, changed, r.ReceiptPath, run, persistLandedFailureAcknowledgement)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("stage=%s error=%v want=%q", stage, err, want)
			}
			if selected != nil && (!errors.Is(err, selected.sentinel) || selected.seen != 2) {
				t.Fatalf("internal cause=%v consumed=%d", err, selected.seen)
			}
			after, e := os.ReadFile(r.ReceiptPath)
			if e != nil || string(after) != string(before) {
				t.Fatalf("historical receipt changed=%v", e)
			}
		})
	}
}
