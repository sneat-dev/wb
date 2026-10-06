package qualityrun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/reposelection"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeBatch() batchOperations {
	return batchOperations{Policy: func(_ string, o quality.RunOptions) (quality.RunOptions, error) { return o, nil }, Cover: func(ctx context.Context, name, path string, o quality.RunOptions) quality.RepositoryCoverage {
		if ctx.Err() != nil {
			panic("normal coverage must retain Background context")
		}
		return quality.RepositoryCoverage{Repository: name, Path: path, Status: quality.StatusPassed, Statements: 10, Covered: 8}
	}, Verify: func(ctx context.Context, name, path string, _ []quality.Check, _ quality.RunOptions) quality.VerificationReport {
		if ctx.Err() != nil {
			panic("normal verification must retain Background context")
		}
		return quality.VerificationReport{Repository: name, Path: path, Status: quality.StatusPassed}
	}, Snapshot: func(string) GitState { return GitState{Revision: strings.Repeat("a", 40), Clean: true} }}
}
func TestWorkflowSelectionAndResumeErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("selection sentinel")
	targets := func(reposelection.Request) ([]reposelection.Target, error) { return nil, boom }
	if _, err := coverageWith(t.Context(), CoverageRequest{}, fakeBatch(), targets); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := verificationWith(t.Context(), VerificationRequest{}, fakeBatch(), targets, time.Now); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	targets = func(reposelection.Request) ([]reposelection.Target, error) { return nil, nil }
	for _, kind := range []string{"coverage", "verify"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			for _, mode := range []string{"no-dir", "missing", "malformed"} {
				t.Run(mode, func(t *testing.T) {
					t.Parallel()
					dir := ""
					if mode != "no-dir" {
						dir = t.TempDir()
					}
					if mode == "malformed" {
						if err := os.WriteFile(filepath.Join(dir, kind+".yaml"), []byte("[broken"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					var err error
					if kind == "coverage" {
						_, err = coverageWith(t.Context(), CoverageRequest{Resume: true, ReportDir: dir}, fakeBatch(), targets)
					} else {
						_, err = verificationWith(t.Context(), VerificationRequest{Resume: true, ReportDir: dir, Name: kind}, fakeBatch(), targets, time.Now)
					}
					if err == nil {
						t.Fatal("resume accepted unusable report")
					}
				})
			}
		})
	}
}
func TestWorkflowObserversPersistenceAndResume(t *testing.T) {
	t.Parallel()
	targets := []reposelection.Target{{Repository: "z", Path: "z"}, {Repository: "a", Path: "a"}}
	selectTargets := func(reposelection.Request) ([]reposelection.Target, error) { return targets, nil }
	for _, kind := range []string{"coverage", "verify"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			var events []string
			observer := Observer{Started: func(n int) {
				if n != 2 {
					t.Errorf("targets=%d", n)
				}
				events = append(events, "start")
			}, Progress: func(p quality.Progress) {
				if p.State != quality.ProgressRepositoryCompleted {
					t.Error(p)
				}
				events = append(events, p.Repository)
			}, Finished: func() {
				events = append(events, "finish")
				if _, err := os.Stat(filepath.Join(dir, kind+".yaml")); !os.IsNotExist(err) {
					t.Errorf("persistence happened before finish: %v", err)
				}
			}}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			ops := fakeBatch()
			now := time.Date(2020, 1, 2, 3, 4, 5, 0, time.FixedZone("fixture", 3600))
			if kind == "coverage" {
				req := CoverageRequest{Selection: reposelection.Request{Parallel: 1}, Observer: observer, ReportDir: dir}
				got, err := coverageWith(ctx, req, ops, selectTargets)
				if err != nil || got.Report.Statements != 20 || got.Artifacts.Report.Path == "" {
					t.Fatalf("result=%+v err=%v", got, err)
				}
				req.Resume = true
				req.Observer = Observer{Started: func(int) { t.Fatal("no-work observer") }}
				got, err = coverageWith(ctx, req, ops, selectTargets)
				if err != nil || !got.NoWork {
					t.Fatalf("resume=%+v %v", got, err)
				}
				old := quality.NewCoverageReport([]quality.RepositoryCoverage{{Repository: "z", Status: quality.StatusFailed}, {Repository: "kept", Status: quality.StatusPassed}})
				if _, err := PersistCoverage(old, dir); err != nil {
					t.Fatal(err)
				}
				req.Observer = Observer{}
				got, err = coverageWith(ctx, req, ops, selectTargets)
				if err != nil || len(got.Report.Repositories) != 2 || got.Report.Repositories[0].Repository != "kept" {
					t.Fatalf("merge=%+v %v", got, err)
				}
			} else {
				req := VerificationRequest{Selection: reposelection.Request{Parallel: 1}, Observer: observer, ReportDir: dir, Name: kind, Checks: []quality.Check{quality.CheckTest}, Profile: "fast"}
				got, err := verificationWith(ctx, req, ops, selectTargets, func() time.Time { return now })
				if err != nil || len(got.Report.Repositories) != 2 || got.Report.Repositories[0].Repository != "a" || got.Report.GeneratedAt.Location() != time.UTC || got.Report.Profile != "fast" {
					t.Fatalf("result=%+v err=%v", got, err)
				}
				req.Resume = true
				req.Observer = Observer{Started: func(int) { t.Fatal("no-work observer") }}
				got, err = verificationWith(ctx, req, ops, selectTargets, time.Now)
				if err != nil || !got.NoWork {
					t.Fatalf("resume=%+v %v", got, err)
				}
				if err := PersistVerification(VerificationIndex{Repositories: []quality.VerificationReport{{Repository: "z", Status: quality.StatusFailed}, {Repository: "kept", Status: quality.StatusPassed}}}, dir, kind); err != nil {
					t.Fatal(err)
				}
				req.Observer = Observer{}
				got, err = verificationWith(ctx, req, ops, selectTargets, time.Now)
				if err != nil || len(got.Report.Repositories) != 2 || got.Report.Repositories[0].Repository != "kept" {
					t.Fatalf("merge=%+v %v", got, err)
				}
			}
			if strings.Join(events, ",") != "start,z,a,finish" {
				t.Fatal(events)
			}
		})
	}
}
func TestWorkflowPropagatesPersistenceFailureAndPublicSelection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	empty := func(reposelection.Request) ([]reposelection.Target, error) { return nil, nil }
	if _, err := coverageWith(t.Context(), CoverageRequest{ReportDir: blocked}, fakeBatch(), empty); err == nil {
		t.Fatal("coverage persistence succeeded")
	}
	if _, err := verificationWith(t.Context(), VerificationRequest{ReportDir: blocked}, fakeBatch(), empty, time.Now); err == nil {
		t.Fatal("verify persistence succeeded")
	}
	if _, err := Coverage(t.Context(), CoverageRequest{Selection: reposelection.Request{Parallel: 0}}); err == nil {
		t.Fatal("public coverage skipped selector validation")
	}
	if _, err := Verification(t.Context(), VerificationRequest{Selection: reposelection.Request{Parallel: 0}}); err == nil {
		t.Fatal("public verification skipped selector validation")
	}
}
func TestSnapshotEffectFailures(t *testing.T) {
	t.Parallel()
	boom := errors.New("git failed")
	for _, stage := range []string{"head", "invalid", "status", "dirty"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			calls := 0
			state := snapshotWith("repo", func(path string, args ...string) ([]byte, error) {
				calls++
				if path != "repo" {
					t.Error(path)
				}
				if calls == 1 {
					if stage == "head" {
						return nil, boom
					}
					if stage == "invalid" {
						return []byte("not-an-object"), nil
					}
					return []byte(strings.Repeat("A", 64) + "\n"), nil
				}
				if stage == "status" {
					return nil, boom
				}
				return []byte(" M changed.go"), nil
			})
			if stage == "dirty" {
				if state.Err != nil || state.Clean || state.Revision != strings.Repeat("a", 64) {
					t.Fatal(state)
				}
			} else if state.Err == nil {
				t.Fatal(state)
			}
		})
	}
}
