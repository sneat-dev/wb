package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func missingCleanupOwnerFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	f, _, r, claims := landedTerminalCleanupFixture(t)
	intent := WorktreeMergeLandOptions{Route: WorktreeMergeRouteAuto, Cleanup: true, OnFailure: "stop"}
	retainWorktreeMergeLandIntent(&r, &intent)
	if e := persistWorktreeMergeReceipt(r); e != nil {
		t.Fatal(e)
	}
	externallyTerminalizeMergeCleanup(t, f, &r)
	if e := os.Remove(terminalWorkLogPath(claims[r.Sources[0].Task])); e != nil {
		t.Fatal(e)
	}
	return f, r
}

//nolint:paralleltest // Genuine Land and terminal-cleanup fixture installs a process-wide GH/PATH provider and WB_PROJECTS_ROOT.
func TestMissingCleanupOwnerNativeObservationAndAppendOnlyStages(t *testing.T) {
	f, r := missingCleanupOwnerFixture(t)
	o := WorktreeMergeMissingCleanupAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Actor: "reviewer", Reason: "actual removed assets and missing terminal log"}
	baseline, e := AcknowledgeMissingWorktreeMergeCleanup(t.Context(), o)
	if e != nil {
		t.Fatalf("genuine missing cleanup baseline=%v", e)
	}
	before, e := os.ReadFile(r.ReceiptPath)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range []struct {
		name string
		args []string
	}{
		{"fetch", []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}},
		{"target revision", []string{"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}},
		{"landing containment", []string{"merge-base", r.LandingSHA, r.Candidate.SHA}},
		{"local branch", []string{"branch", "--list", "--format=%(refname:short)", r.Candidate.Branch}},
		{"remote branch", []string{"ls-remote", "--heads", "origin", "refs/heads/" + r.Candidate.Branch}},
	} {
		t.Run(row.name, func(t *testing.T) {
			sentinel := errors.New("selected missing cleanup " + row.name)
			run := &landedFailureOwnerRunner{Runner: defaultRunner, path: f.canonical, args: row.args, ordinal: 1, sentinel: sentinel}
			_, e := acknowledgeMissingWorktreeMergeCleanup(t.Context(), o, run, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, persistMissingCleanupAcknowledgement)
			if !errors.Is(e, sentinel) || run.seen != 1 {
				t.Fatalf("exact %s error=%v consumed=%d", row.name, e, run.seen)
			}
		})
	}
	t.Run("hash", func(t *testing.T) {
		sentinel := errors.New("selected native hash read")
		consumed := false
		_, e := acknowledgeMissingWorktreeMergeCleanup(t.Context(), o, defaultRunner, readWorktreeMergeReceipt, func(path string) (string, error) {
			if path != r.ReceiptPath {
				t.Fatalf("hash path=%s", path)
			}
			consumed = true
			return "", sentinel
		}, persistMissingCleanupAcknowledgement)
		if !consumed || !errors.Is(e, sentinel) {
			t.Fatalf("hash=%v consumed=%t", e, consumed)
		}
	})
	t.Run("persist refusal", func(t *testing.T) {
		o.Apply = true
		sentinel := errors.New("selected durable ack refusal")
		consumed := false
		_, e := acknowledgeMissingWorktreeMergeCleanup(t.Context(), o, defaultRunner, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, func(path string, a WorktreeMergeMissingCleanupAcknowledgement) error {
			if path != baseline.AcknowledgementPath || a.ReceiptSHA256 != baseline.ReceiptSHA256 {
				t.Fatalf("wrong native save identity: %s %+v", path, a)
			}
			consumed = true
			return sentinel
		})
		if !consumed || !errors.Is(e, sentinel) {
			t.Fatalf("persist=%v consumed=%t", e, consumed)
		}
		o.Apply = false
	})
	t.Run("native apply and collision", func(t *testing.T) {
		o.Apply = true
		first, e := AcknowledgeMissingWorktreeMergeCleanup(t.Context(), o)
		if e != nil {
			t.Fatal(e)
		}
		second, e := AcknowledgeMissingWorktreeMergeCleanup(t.Context(), o)
		if e != nil || second.ID != first.ID {
			t.Fatalf("native Link collision reauthentication=%+v %v", second, e)
		}
		info, e := os.Stat(first.AcknowledgementPath)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("native mode=%v %v", info, e)
		}
		o.Apply = false
	})
	after, e := os.ReadFile(r.ReceiptPath)
	if e != nil || string(after) != string(before) {
		t.Fatalf("native historical receipt changed: %v", e)
	}
}

//nolint:paralleltest // Genuine cleanup/provider setup is process-wide; reversible rows reuse one native terminal proof baseline.
func TestMissingCleanupOwnerPhysicalAbsenceAndReauthentication(t *testing.T) {
	f, r := missingCleanupOwnerFixture(t)
	o := WorktreeMergeMissingCleanupAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "native removed assets"}
	ack, e := AcknowledgeMissingWorktreeMergeCleanup(t.Context(), o)
	if e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(r.ReceiptPath)
	if e != nil {
		t.Fatal(e)
	}
	for _, stage := range []string{"checkout reappears", "stat error", "target branch", "malformed sidecar", "receipt changes after sidecar read"} {
		t.Run(stage, func(t *testing.T) {
			changed := r
			changed.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			switch stage {
			case "checkout reappears":
				t.Cleanup(func() { _ = os.RemoveAll(r.Candidate.Worktree) })
				if e := os.MkdirAll(r.Candidate.Worktree, 0700); e != nil {
					t.Fatal(e)
				}
				_, e := inspectMissingWorktreeMergeCleanup(t.Context(), defaultRunner, worktreeMergeReceiptSHA256, f.githubDir, changed, o.Actor, o.Reason, 0, 0)
				if e == nil || !strings.Contains(e.Error(), "worktree") {
					t.Fatalf("actual reappearance=%v", e)
				}
			case "stat error":
				blocker := filepath.Join(t.TempDir(), "blocker")
				if e := os.WriteFile(blocker, []byte("file"), 0600); e != nil {
					t.Fatal(e)
				}
				changed.Candidate.Worktree = filepath.Join(blocker, "candidate")
				_, e := inspectMissingWorktreeMergeCleanup(t.Context(), defaultRunner, worktreeMergeReceiptSHA256, f.githubDir, changed, o.Actor, o.Reason, 0, 0)
				var pe *os.PathError
				if !errors.As(e, &pe) || pe.Path != changed.Candidate.Worktree {
					t.Fatalf("native stat=%v", e)
				}
			case "target branch":
				assets, e := terminalWorkLogExpectations(r)
				if e != nil {
					t.Fatal(e)
				}
				assets[0].Branch = r.Target
				e = requireTerminalCleanupBranchesAbsentWithRunner(t.Context(), defaultRunner, f.githubDir, r, assets, 0, 0)
				if e == nil || !strings.Contains(e.Error(), "branch is the target") {
					t.Fatalf("target branch=%v", e)
				}
			case "malformed sidecar":
				bytes, e := os.ReadFile(ack.AcknowledgementPath)
				if e != nil {
					t.Fatal(e)
				}
				restore := func() {
					if e := os.WriteFile(ack.AcknowledgementPath, bytes, 0600); e != nil {
						t.Error(e)
					}
				}
				t.Cleanup(restore)
				if e := os.WriteFile(ack.AcknowledgementPath, []byte("{invalid"), 0600); e != nil {
					t.Fatal(e)
				}
				_, e = validateMissingCleanupAcknowledgement(t.Context(), f.githubDir, r, ack.AcknowledgementPath, 0, 0)
				restore()
				if e == nil {
					t.Fatal("malformed native sidecar accepted")
				}
			case "receipt changes after sidecar read":
				restore := func() {
					if e := os.WriteFile(r.ReceiptPath, before, 0600); e != nil {
						t.Error(e)
					}
				}
				t.Cleanup(restore)
				consumed := false
				value, e := validateMissingCleanupAcknowledgementWithRunner(t.Context(), defaultRunner, func(path string) (string, error) {
					consumed = true
					if e := os.WriteFile(path, append(append([]byte(nil), before...), '\n'), 0600); e != nil {
						return "", e
					}
					return worktreeMergeReceiptSHA256(path)
				}, f.githubDir, r, ack.AcknowledgementPath, 0, 0)
				restore()
				if !consumed || e == nil || !strings.Contains(e.Error(), "no longer matches") || value.ID != ack.ID {
					t.Fatalf("native changed hash reauthentication=%+v %v consumed=%t", value, e, consumed)
				}
			}
		})
	}
}

func TestMissingCleanupBranchAbsenceUsesActualPrivateRefs(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"canonical", "local branch", "remote branch"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f, r := landedFailureOwnerFixture(t)
			assets, e := terminalWorkLogExpectations(r)
			if e != nil {
				t.Fatal(e)
			}
			assets = assets[:1]
			want := ""
			switch stage {
			case "canonical":
				r.Repository = "invalid"
				want = "repository"
			case "local branch":
				want = "local branch"
			case "remote branch":
				assets[0].Branch = "private-missing-cleanup-remote"
				runEngineGit(t, f.canonical, "push", "origin", r.Candidate.SHA+":refs/heads/"+assets[0].Branch)
				want = "remote branch"
			}
			e = requireTerminalCleanupBranchesAbsentWithRunner(t.Context(), defaultRunner, f.githubDir, r, assets, 0, 0)
			if e == nil || !strings.Contains(e.Error(), want) {
				t.Fatalf("real %s=%v", stage, e)
			}
		})
	}
}
