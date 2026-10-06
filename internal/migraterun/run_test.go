package migraterun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/migrate"
	"github.com/sneat-dev/wb/internal/progress"
	"reflect"
	"testing"
)

func TestLocalOrdersEffectsAndPreservesFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("effect refused")
	for _, failAt := range []string{"", "load", "plan", "apply", "write"} {
		t.Run(failAt, func(t *testing.T) {
			t.Parallel()
			var calls []string
			effect := func(name string) error {
				calls = append(calls, name)
				if name == failAt {
					return failure
				}
				return nil
			}
			ops := Operations{
				Load: func(path string) (migrate.Spec, error) {
					if path != "spec" {
						t.Fatal(path)
					}
					return migrate.Spec{ID: "migration"}, effect("load")
				},
				BuildPlan: func(spec migrate.Spec, roots ...string) (migrate.Plan, error) {
					if spec.ID != "migration" || !reflect.DeepEqual(roots, []string{"root"}) {
						t.Fatal(spec, roots)
					}
					return migrate.Plan{Changes: []migrate.FileChange{{Path: "value.go"}}}, effect("plan")
				},
				Apply: func(plan migrate.Plan) error {
					if len(plan.Changes) != 1 {
						t.Fatal(plan)
					}
					return effect("apply")
				},
				WriteReports: func(dir string, report migrate.Report) error {
					if dir != "reports" || report.Status != "applied" {
						t.Fatal(dir, report)
					}
					return effect("write")
				},
			}
			result, err := ops.Local(t.Context(), LocalRequest{SpecPath: "spec", Roots: []string{"root"}, Apply: true, ReportDir: "reports"})
			if failAt != "" {
				if !errors.Is(err, failure) {
					t.Fatalf("error=%v", err)
				}
				want := []string{"load", "plan", "apply", "write"}
				for i, c := range want {
					if c == failAt {
						want = want[:i+1]
						break
					}
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatal(calls, want)
				}
			} else {
				if err != nil || !result.HasChanges || result.Report.Status != "applied" {
					t.Fatal(result, err)
				}
				if !reflect.DeepEqual(calls, []string{"load", "plan", "apply", "write"}) {
					t.Fatal(calls)
				}
			}
		})
	}
}
func TestLocalPlanWithoutChangesOrWrites(t *testing.T) {
	t.Parallel()
	ops := Operations{Load: func(string) (migrate.Spec, error) { return migrate.Spec{}, nil }, BuildPlan: func(migrate.Spec, ...string) (migrate.Plan, error) { return migrate.Plan{}, nil }}
	got, err := ops.Local(t.Context(), LocalRequest{})
	if err != nil || got.HasChanges || got.Report.Status != "planned" {
		t.Fatal(got, err)
	}
}

func TestCampaignOrderingAndErrorPrecedence(t *testing.T) {
	t.Parallel()
	failure := errors.New("effect refused")
	runFailure := errors.New("campaign failed")
	for _, failAt := range []string{"", "load", "home", "run", "persist"} {
		t.Run(failAt, func(t *testing.T) {
			t.Parallel()
			var calls []string
			effect := func(name string) error {
				calls = append(calls, name)
				if name == failAt {
					return failure
				}
				return nil
			}
			reporter := func(progress.Event) {}
			ops := Operations{
				Load: func(string) (migrate.Spec, error) { return migrate.Spec{ID: "migration"}, effect("load") },
				EnsureRoot: func(root string) (string, error) {
					if root != "github" {
						t.Fatal(root)
					}
					return "home", effect("home")
				},
				RunCampaign: func(spec migrate.Spec, root string, opt migrate.CampaignOptions) (migrate.CampaignReport, error) {
					calls = append(calls, "run")
					if root != "source" || spec.ID != "migration" || opt.ReportDir != "home/reports/migration" || opt.Ref != "main" || opt.Verify != migrate.VerifyNone || !opt.Apply || !opt.Resume || !opt.Commit || !opt.Push || !opt.PR || !opt.Merge || opt.Parallel != 3 || opt.ModuleRefs["module"] != "ref" || opt.Progress == nil {
						t.Fatal(spec, root, opt)
					}
					opt.Progress(progress.Event{Operation: spec.ID})
					if failAt == "run" || failAt == "persist" {
						return migrate.CampaignReport{}, runFailure
					}
					return migrate.CampaignReport{Status: "completed"}, nil
				},
				WriteCampaignReports: func(dir string, report migrate.CampaignReport) error {
					if dir != "home/reports/migration" {
						t.Fatal(dir)
					}
					return effect("persist")
				},
			}
			request := CampaignRequest{SpecPath: "spec", Roots: []string{"source"}, GitHubDir: "github", Ref: "main", ModuleRefs: map[string]string{"module": "ref"}, Verify: migrate.VerifyNone, Apply: true, Resume: true, Commit: true, Push: true, PR: true, Merge: true, Parallel: 3,
				PrepareProgress: func(id string) progress.Reporter {
					if id != "migration" {
						t.Fatal(id)
					}
					calls = append(calls, "ready")
					return reporter
				},
				BeforePersist: func(done CampaignCompletion) {
					if done.SpecID != "migration" || done.Failed != (failAt == "run" || failAt == "persist") {
						t.Fatal(done)
					}
					calls = append(calls, "finish")
				}}
			got, err := ops.Campaign(t.Context(), request)
			switch failAt {
			case "load":
				if !errors.Is(err, failure) || !reflect.DeepEqual(calls, []string{"load"}) {
					t.Fatal(err, calls)
				}
			case "home":
				if !errors.Is(err, failure) || !reflect.DeepEqual(calls, []string{"load", "home"}) {
					t.Fatal(err, calls)
				}
			default:
				if !reflect.DeepEqual(calls, []string{"load", "home", "ready", "run", "finish", "persist"}) {
					t.Fatal(calls)
				}
				if failAt == "persist" {
					if !errors.Is(err, failure) {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				} else if failAt == "run" {
					if got.RunError != runFailure {
						t.Fatal(got)
					}
				} else if got.Report.Status != "completed" || got.RunError != nil {
					t.Fatal(got)
				}
			}
		})
	}
}
func TestCampaignExplicitReportsNeedNoProgressOrHome(t *testing.T) {
	t.Parallel()
	ops := Operations{Load: func(string) (migrate.Spec, error) { return migrate.Spec{}, nil }, RunCampaign: func(_ migrate.Spec, _ string, opt migrate.CampaignOptions) (migrate.CampaignReport, error) {
		if opt.ReportDir != "explicit" || opt.Progress != nil {
			t.Fatal(opt)
		}
		return migrate.CampaignReport{}, nil
	}, WriteCampaignReports: func(string, migrate.CampaignReport) error { return nil }}
	if _, err := ops.Campaign(context.Background(), CampaignRequest{Roots: []string{"source"}, ReportDir: "explicit"}); err != nil {
		t.Fatal(err)
	}
}
func TestCampaignRootCountsFollowLoadAndCleanupDoesNotCreateReports(t *testing.T) {
	t.Parallel()
	for _, cleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "campaign", true: "cleanup"}[cleanup], func(t *testing.T) {
			t.Parallel()
			loaded := false
			ops := Operations{Load: func(string) (migrate.Spec, error) { loaded = true; return migrate.Spec{ID: "migration"}, nil }}
			roots := []string{}
			if cleanup {
				roots = []string{"source"}
			}
			_, err := ops.Campaign(t.Context(), CampaignRequest{Cleanup: cleanup, Roots: roots})
			if err == nil || !loaded {
				t.Fatal(err, loaded)
			}
		})
	}
	failure := errors.New("cleanup refused")
	for _, fail := range []bool{false, true} {
		ops := Operations{Load: func(string) (migrate.Spec, error) { return migrate.Spec{ID: "migration"}, nil }, CleanupCampaignWorktrees: func(root, id string) ([]string, error) {
			if root != "github" || id != "migration" {
				t.Fatal(root, id)
			}
			if fail {
				return nil, failure
			}
			return []string{"worktree"}, nil
		}}
		got, err := ops.Campaign(t.Context(), CampaignRequest{Cleanup: true, GitHubDir: "github"})
		if !got.Cleanup {
			t.Fatal(got)
		}
		if fail {
			if err != failure {
				t.Fatal(err)
			}
		} else if err != nil || !reflect.DeepEqual(got.Removed, []string{"worktree"}) {
			t.Fatal(got, err)
		}
	}
}
