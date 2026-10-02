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

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/worktreebranches"
)

func quarantineNextFixture(t *testing.T) (BranchQuarantineOptions, string, branchQuarantinePlanOps) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(root, "acme", "app")
	if err := os.MkdirAll(clone, 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, clone, "init", "--initial-branch=main")
	seedFreshFixtureGitConfig(t, clone, false)
	gitTest(t, clone, "commit", "--allow-empty", "-m", "initial")
	gitTest(t, clone, "branch", "feature/old")
	sha := gitTestOutput(t, clone, "rev-parse", "feature/old")
	ops := realBranchQuarantineOps()
	// No network evidence is acquired by this fixture. Git, linked checkout,
	// claim inventory and ref CAS remain the real local implementations.
	ops.pullRequests = func(context.Context, string, string, string) ([]githubPullRequest, error) { return nil, nil }
	ops.openBasePull = func(context.Context, string, string, string) (*PullRequest, error) { return nil, nil }
	return BranchQuarantineOptions{ProjectsRoot: root, Repository: "acme/app", Branch: "feature/old", SHA: sha, Reason: "verified obsolete", Apply: true, ReportDir: filepath.Join(root, "reports", "run"), Now: func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }}, clone, ops
}

func quarantineNextReadReport(t *testing.T, path string) BranchQuarantineOutcome {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report BranchQuarantineOutcome
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestE2EQuarantineRetainsEveryDurableCheckpoint(t *testing.T) {
	t.Parallel()
	for _, checkpoint := range []int{0, 1, 2} {
		t.Run(string(rune('0'+checkpoint)), func(t *testing.T) {
			t.Parallel()
			options, clone, ops := quarantineNextFixture(t)
			failure := errors.New("checkpoint publication failed")
			inj := &filewrite.Injector{Step: filewrite.StepRename, Skip: checkpoint, Err: failure}
			outcome, err := branchQuarantineWithOps(context.Background(), options, ops, inj)
			if !errors.Is(err, failure) || len(outcome.Results) != 1 {
				t.Fatalf("checkpoint%d outcome=%+v error=%v", checkpoint, outcome, err)
			}
			result := outcome.Results[0]
			moved := checkpoint == 2
			if got := gitRefExists(clone, "refs/heads/"+options.Branch); got == moved {
				t.Fatalf("source exists=%v, moved=%v", got, moved)
			}
			if got := gitRefExists(clone, "refs/heads/"+result.Destination); got != moved {
				t.Fatalf("destination exists=%v, moved=%v", got, moved)
			}
			want := "planned"
			if moved {
				want = "quarantined"
			}
			if result.Outcome != want || result.SHA != options.SHA || result.Reason != options.Reason {
				t.Fatalf("returned result=%+v", result)
			}
			if checkpoint == 0 {
				if _, err := os.Stat(outcome.ReportPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("premature published report: %v", err)
				}
			} else {
				report := quarantineNextReadReport(t, outcome.ReportPath)
				if len(report.Results) != 1 || report.Results[0].Outcome != "planned" || report.Results[0].SHA != options.SHA || report.Results[0].Destination != result.Destination || report.Results[0].Reason != options.Reason {
					t.Fatalf("durable recovery record=%+v", report)
				}
			}
		})
	}
}

func TestE2EQuarantineCheckpointsKeepAdmissionAndCASIndependent(t *testing.T) {
	t.Parallel()
	t.Run("success and default report", func(t *testing.T) {
		t.Parallel()
		options, clone, ops := quarantineNextFixture(t)
		options.ReportDir = ""
		outcome, err := branchQuarantineWithOps(context.Background(), options, ops, nil)
		if err != nil || len(outcome.Results) != 1 || outcome.Results[0].Outcome != "quarantined" {
			t.Fatalf("apply=%+v %v", outcome, err)
		}
		report := quarantineNextReadReport(t, outcome.ReportPath)
		if report.Results[0] != outcome.Results[0] || !report.GeneratedAt.Equal(options.Now()) || !strings.HasPrefix(outcome.ReportPath, filepath.Join(options.ProjectsRoot, ".wb", "reports", "branch-quarantine")) {
			t.Fatalf("durable final report=%+v", report)
		}
		if gitRefExists(clone, "refs/heads/"+options.Branch) || gitTestOutput(t, clone, "rev-parse", outcome.Results[0].Destination) != options.SHA {
			t.Fatal("CAS did not preserve exact source commit")
		}
	})
	t.Run("collision refuses all before apply", func(t *testing.T) {
		t.Parallel()
		options, clone, ops := quarantineNextFixture(t)
		refs := []string{"feature/a-b", "feature/a/b"}
		entries := make([]BranchQuarantineRequest, 0, 2)
		for _, ref := range refs {
			gitTest(t, clone, "branch", ref)
			entries = append(entries, BranchQuarantineRequest{Repository: options.Repository, Ref: ref, SHA: options.SHA, Reason: options.Reason})
		}
		raw, err := json.Marshal(BranchQuarantineManifest{Entries: entries})
		if err != nil {
			t.Fatal(err)
		}
		manifest := filepath.Join(options.ProjectsRoot, "manifest.json")
		if err := os.WriteFile(manifest, raw, 0600); err != nil {
			t.Fatal(err)
		}
		options.Manifest = manifest
		options.Repository = ""
		options.Branch = ""
		options.SHA = ""
		options.Reason = ""
		// A second checkpoint would fail; all refused rows must be skipped.
		outcome, err := branchQuarantineWithOps(context.Background(), options, ops, &filewrite.Injector{Step: filewrite.StepRename, Skip: 1, Err: errors.New("unexpected second publication")})
		if err != nil || len(outcome.Results) != 2 {
			t.Fatalf("collision=%+v %v", outcome, err)
		}
		for _, r := range outcome.Results {
			if r.Outcome != "refused" || !strings.Contains(r.Error, "destinations collide") || !gitRefExists(clone, "refs/heads/"+r.Ref) {
				t.Fatalf("admission result=%+v", r)
			}
		}
	})
	t.Run("destination appears after preCAS checkpoint", func(t *testing.T) {
		t.Parallel()
		options, clone, ops := quarantineNextFixture(t)
		destination := worktreebranches.RetiredBranchDestination(options.Now(), options.Branch, options.SHA)
		inj := &filewrite.Injector{Step: filewrite.StepRename, Skip: 1, Hook: func() { gitTest(t, clone, "branch", destination) }}
		outcome, err := branchQuarantineWithOps(context.Background(), options, ops, inj)
		if err != nil || len(outcome.Results) != 1 || outcome.Results[0].Outcome != "failed" || !strings.Contains(outcome.Results[0].Error, "destination appeared") {
			t.Fatalf("late collision=%+v %v", outcome, err)
		}
		if !gitRefExists(clone, "refs/heads/"+options.Branch) {
			t.Fatal("late collision removed source")
		}
		if report := quarantineNextReadReport(t, outcome.ReportPath); report.Results[0] != outcome.Results[0] {
			t.Fatalf("failed apply report=%+v", report)
		}
	})
	t.Run("source moves at atomic CAS", func(t *testing.T) {
		t.Parallel()
		options, clone, ops := quarantineNextFixture(t)
		gitTest(t, clone, "commit", "--allow-empty", "-m", "second")
		replacement := gitTestOutput(t, clone, "rev-parse", "main")
		ops.rename = func(ctx context.Context, path, source, destination, sha string) error {
			gitTest(t, clone, "update-ref", "refs/heads/"+source, replacement, sha)
			return atomicLocalBranchRename(ctx, path, source, destination, sha)
		}
		outcome, err := branchQuarantineWithOps(context.Background(), options, ops, nil)
		if err != nil || len(outcome.Results) != 1 || outcome.Results[0].Outcome != "failed" || !strings.Contains(outcome.Results[0].Error, "local CAS rename:") {
			t.Fatalf("CAS race=%+v %v", outcome, err)
		}
		if gitTestOutput(t, clone, "rev-parse", options.Branch) != replacement || gitRefExists(clone, "refs/heads/"+outcome.Results[0].Destination) {
			t.Fatal("CAS failure changed competing refs")
		}
		if report := quarantineNextReadReport(t, outcome.ReportPath); report.Results[0] != outcome.Results[0] {
			t.Fatalf("CAS failure report=%+v", report)
		}
	})
}

func TestE2EQuarantineAdmissionFailuresPreserveZeroOrPlannedOutcome(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"root", "request", "discovery", "unsafe report", "occupied report", "parent file", "invalid JSON time", "dry run"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			options, clone, ops := quarantineNextFixture(t)
			wantZero := false
			wantErr := true
			switch name {
			case "root":
				options.ProjectsRoot = ""
				wantZero = true
			case "request":
				options.Reason = ""
				wantZero = true
			case "discovery":
				options.Repository = "other/missing"
				wantZero = true
			case "unsafe report":
				options.ReportDir = filepath.Join(clone, "reports")
			case "occupied report":
				if err := os.MkdirAll(options.ReportDir, 0700); err != nil {
					t.Fatal(err)
				}
			case "parent file":
				if err := os.WriteFile(filepath.Dir(options.ReportDir), []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid JSON time":
				options.Now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
			case "dry run":
				options.Apply = false
				options.Now = nil
				wantErr = false
			}
			outcome, err := branchQuarantineWithOps(context.Background(), options, ops, nil)
			if (err != nil) != wantErr {
				t.Fatalf("%s outcome=%+v error=%v", name, outcome, err)
			}
			if wantZero {
				if !outcome.GeneratedAt.IsZero() || outcome.Results != nil || outcome.ReportPath != "" {
					t.Fatalf("refusal returned nonzero outcome=%+v", outcome)
				}
			} else if len(outcome.Results) != 1 || outcome.Results[0].Outcome != "planned" {
				t.Fatalf("planned refusal=%+v", outcome)
			}
			if !gitRefExists(clone, "refs/heads/"+options.Branch) {
				t.Fatal("admission failure changed source")
			}
		})
	}
	// The public real-operation wrapper is reached without any network boundary.
	if outcome, err := BranchQuarantine(context.Background(), BranchQuarantineOptions{}); err == nil || outcome.Results != nil {
		t.Fatalf("public missing-root result=%+v %v", outcome, err)
	}
}
