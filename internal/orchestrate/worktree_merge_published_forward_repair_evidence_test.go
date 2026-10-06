package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// These cases exercise the second evidence read made under the lane lock. The
// caller may have inspected a valid repair plan before any of these inputs
// changed, but must refuse to create a candidate after the change.
func TestPublishedForwardRepairRevalidationRefusesChangedEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		change  func(*testing.T, WorktreeMergeReceipt, *WorktreeMergePublishedForwardRepairOptions)
		wantErr string
	}{
		{
			name: "receipt identity",
			change: func(t *testing.T, receipt WorktreeMergeReceipt, _ *WorktreeMergePublishedForwardRepairOptions) {
				t.Helper()
				if err := os.WriteFile(receipt.ReceiptPath, []byte("invalid receipt\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "failed receipt changed",
		},
		{
			name: "receipt digest",
			change: func(t *testing.T, receipt WorktreeMergeReceipt, _ *WorktreeMergePublishedForwardRepairOptions) {
				t.Helper()
				contents, err := os.ReadFile(receipt.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(receipt.ReceiptPath, append(contents, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "failed receipt SHA256 changed",
		},
		{
			name: "expected receipt digest",
			change: func(_ *testing.T, _ WorktreeMergeReceipt, options *WorktreeMergePublishedForwardRepairOptions) {
				options.ExpectedReceiptSHA256 = strings.Repeat("0", 64)
			},
			wantErr: "failed receipt SHA256 changed",
		},
		{
			name: "supersession digest",
			change: func(t *testing.T, receipt WorktreeMergeReceipt, _ *WorktreeMergePublishedForwardRepairOptions) {
				t.Helper()
				path := validationFailureSupersessionPath(receipt.ReceiptPath)
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(contents, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "self-supersession acknowledgement changed",
		},
		{
			name: "source head",
			change: func(t *testing.T, _ WorktreeMergeReceipt, options *WorktreeMergePublishedForwardRepairOptions) {
				t.Helper()
				writeEngineFile(t, filepath.Join(options.Sources[1], "advanced.txt"), "advanced\n")
				runEngineGit(t, options.Sources[1], "add", "advanced.txt")
				runEngineGit(t, options.Sources[1], "commit", "-m", "test: advance repair source")
			},
			wantErr: "current repair source slice, repository, or canonical identity changed",
		},
		{
			name: "expected source head",
			change: func(_ *testing.T, _ WorktreeMergeReceipt, options *WorktreeMergePublishedForwardRepairOptions) {
				options.ExpectedSourceSHAs[1] = strings.Repeat("0", 40)
			},
			wantErr: "revalidate current repair source evidence",
		},
		{
			name: "remote target",
			change: func(t *testing.T, receipt WorktreeMergeReceipt, options *WorktreeMergePublishedForwardRepairOptions) {
				t.Helper()
				canonical, err := worktrees.CanonicalRepositoryPath(options.ProjectsRoot, receipt.Repository)
				if err != nil {
					t.Fatal(err)
				}
				writeEngineFile(t, filepath.Join(canonical, "target-drift.txt"), "advanced\n")
				runEngineGit(t, canonical, "add", "target-drift.txt")
				runEngineGit(t, canonical, "commit", "-m", "test: advance repair target")
				runEngineGit(t, canonical, "push", "origin", receipt.Target)
			},
			wantErr: "remote target drifted",
		},
	}

	runCase := func(t *testing.T, index int, fixture engineFixture, receipt WorktreeMergeReceipt, baseOptions WorktreeMergePublishedForwardRepairOptions, shared bool) {
		t.Helper()
		test := tests[index]
		options := baseOptions
		options.Sources = append([]string(nil), baseOptions.Sources...)
		options.ExpectedSourceSHAs = append([]string(nil), baseOptions.ExpectedSourceSHAs...)
		sources, repository, canonical, err := inspectPublishedForwardRepairSourcesWithRunner(context.Background(), defaultRunner, options.ProjectsRoot, options.Sources, receipt.Target)
		if err != nil {
			t.Fatal(err)
		}
		var restore func() error
		if shared {
			receiptBytes, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			receiptInfo, err := os.Stat(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			sidecarPath := validationFailureSupersessionPath(receipt.ReceiptPath)
			sidecarBytes, err := os.ReadFile(sidecarPath)
			if err != nil {
				t.Fatal(err)
			}
			sidecarInfo, err := os.Stat(sidecarPath)
			if err != nil {
				t.Fatal(err)
			}
			restored := false
			restore = func() error {
				for _, record := range []struct {
					path     string
					contents []byte
					mode     os.FileMode
				}{
					{receipt.ReceiptPath, receiptBytes, receiptInfo.Mode().Perm()},
					{sidecarPath, sidecarBytes, sidecarInfo.Mode().Perm()},
				} {
					if err := os.WriteFile(record.path, record.contents, record.mode); err != nil {
						return err
					}
					if err := os.Chmod(record.path, record.mode); err != nil {
						return err
					}
					got, err := os.ReadFile(record.path)
					if err != nil {
						return err
					}
					info, err := os.Stat(record.path)
					if err != nil {
						return err
					}
					if string(got) != string(record.contents) || info.Mode().Perm() != record.mode {
						return fmt.Errorf("restored record bytes/mode differ: %s", record.path)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				baselineSources, baselineRepository, baselineCanonical, err := inspectPublishedForwardRepairSourcesWithRunner(ctx, defaultRunner, baseOptions.ProjectsRoot, baseOptions.Sources, receipt.Target)
				if err != nil {
					return err
				}
				if err := revalidatePublishedForwardRepairEvidence(ctx, baseOptions, defaultRunner, worktreeMergeReceiptSHA256, os.ReadFile, receipt.ReceiptPath,
					baseOptions.ExpectedReceiptSHA256, baseOptions.ExpectedImmutableClaimSHA256, baseOptions.ExpectedSupersessionSHA256,
					"", baselineSources, baselineRepository, baselineCanonical, baseOptions.ExpectedCurrentTargetSHA); err != nil {
					return err
				}
				restored = true
				return nil
			}
			t.Cleanup(func() {
				if !restored {
					if err := restore(); err != nil {
						t.Errorf("restore shared native baseline: %v", err)
					}
				}
			})
		}
		test.change(t, receipt, &options)
		err = revalidatePublishedForwardRepairEvidence(context.Background(), options, defaultRunner, worktreeMergeReceiptSHA256, os.ReadFile, receipt.ReceiptPath,
			options.ExpectedReceiptSHA256, options.ExpectedImmutableClaimSHA256, options.ExpectedSupersessionSHA256,
			"", sources, repository, canonical, options.ExpectedCurrentTargetSHA)
		if err == nil || !strings.Contains(err.Error(), test.wantErr) {
			t.Fatalf("revalidate after %s: %v, want %q", test.name, err, test.wantErr)
		}
		assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
		if restore != nil {
			if err := restore(); err != nil {
				t.Fatalf("restore shared native baseline: %v", err)
			}
		}
	}
	t.Run("records and options", func(t *testing.T) {
		t.Parallel()
		fixture, receipt, _, options := publishedForwardRepairFixtureWithFixture(t, newExplicitRootEngineFixture(t))
		for _, index := range []int{0, 1, 2, 3, 5} {
			//nolint:paralleltest // These five reversible byte/option rows share one private fixture; restoration completes before the next row.
			t.Run(tests[index].name, func(t *testing.T) { runCase(t, index, fixture, receipt, options, true) })
		}
	})
	for _, index := range []int{4, 6} {
		t.Run(tests[index].name, func(t *testing.T) {
			t.Parallel()
			fixture, receipt, _, options := publishedForwardRepairFixtureWithFixture(t, newExplicitRootEngineFixture(t))
			runCase(t, index, fixture, receipt, options, false)
		})
	}
}

//nolint:paralleltest // selfSupersessionFixture calls t.Setenv through newEngineFixture
func TestPublishedForwardRepairRevalidationBindsExistingCorrection(t *testing.T) {
	fixture, receipt, replacement, supersession, claimHash := selfSupersessionFixture(t)
	supersessionHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	correction, err := CorrectValidationFailedSelfSupersession(context.Background(), WorktreeMergeSelfSupersessionCorrectionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
		ExpectedSupersessionSHA256: supersessionHash, ExpectedImmutableClaimSHA256: claimHash,
		Apply: true, Actor: "reviewer", Reason: "bind a previously recorded correction during forward repair",
	})
	if err != nil {
		t.Fatal(err)
	}
	correctionHash, err := worktreeMergeReceiptSHA256(correction.CorrectionPath)
	if err != nil {
		t.Fatal(err)
	}
	currentTarget, err := fetchExactMergeTarget(context.Background(), receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	options := WorktreeMergePublishedForwardRepairOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
		Sources: []string{receipt.Sources[0].Worktree}, ExpectedSourceSHAs: []string{receipt.Sources[0].SHA},
		ExpectedReceiptSHA256: receiptHash, ExpectedImmutableClaimSHA256: claimHash,
		ExpectedSupersessionSHA256: supersessionHash, ExpectedCurrentTargetSHA: currentTarget,
		Actor: "reviewer", Reason: "retain the recorded correction as a required root",
	}
	plan, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	foundReplacement := false
	for _, root := range plan.RequiredRoots {
		if root.Kind == "corrected_replacement" && root.SHA == correction.CorrectedReplacement.SHA {
			foundReplacement = true
		}
	}
	if !foundReplacement {
		t.Fatalf("corrected replacement absent from planned roots: %+v", plan.RequiredRoots)
	}
	sources, repository, canonical, err := inspectPublishedForwardRepairSourcesWithRunner(context.Background(), defaultRunner, options.ProjectsRoot, options.Sources, receipt.Target)
	if err != nil {
		t.Fatal(err)
	}
	revalidate := func() error {
		return revalidatePublishedForwardRepairEvidence(context.Background(), options, defaultRunner, worktreeMergeReceiptSHA256, os.ReadFile, receipt.ReceiptPath,
			receiptHash, claimHash, supersessionHash, correctionHash, sources, repository, canonical, currentTarget)
	}
	if err := revalidate(); err != nil {
		t.Fatalf("unchanged correction refused: %v", err)
	}
	contents, err := os.ReadFile(correction.CorrectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(correction.CorrectionPath, append(contents, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := revalidate(); err == nil || !strings.Contains(err.Error(), "self-supersession correction changed") {
		t.Fatalf("changed correction revalidation error = %v", err)
	}
	if err := os.Remove(correction.CorrectionPath); err != nil {
		t.Fatal(err)
	}
	if err := revalidate(); err == nil || !strings.Contains(err.Error(), "self-supersession correction disappeared") {
		t.Fatalf("missing correction revalidation error = %v", err)
	}
	assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
}
