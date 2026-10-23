package depsrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/progress"
)

func TestSelectionKeepsFiltersMetadataAndNativeIdentityPriority(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("selection refused")
	ctx := context.Background()
	for _, test := range []struct {
		name    string
		request Selection
		want    string
	}{
		{"pool", Selection{}, "parallelism"}, {"retry", Selection{Parallel: 1, Retry: -1}, "retry"}, {"timeout", Selection{Parallel: 1, Timeout: -1}, "timeout"}, {"regex", Selection{Parallel: 1, Regex: "["}, "invalid --regex"}, {"glob", Selection{Parallel: 1, Match: "["}, "invalid --match"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(Dependencies{}).Select(ctx, test.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation=%v", err)
			}
		})
	}
	for _, stage := range []string{"abs", "identity", "fleet", "glob", "regex", "filter", "empty"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			request := Selection{ProjectsRoot: "/private/fixture", RepositoryPath: "checkout", Parallel: 1, Fleet: stage == "fleet" || stage == "empty"}
			if stage == "glob" {
				request.Match = "other/*"
			}
			if stage == "regex" {
				request.Regex = "^other/"
			}
			if stage == "filter" {
				request.Filter = "other"
			}
			d := Dependencies{Abs: func(path string) (string, error) {
				if path != "checkout" {
					t.Fatalf("path=%q", path)
				}
				if stage == "abs" {
					return "", sentinel
				}
				return "/resolved", nil
			}, Identity: func(path, root string) (string, string, error) {
				if path != "/resolved" || root != request.ProjectsRoot {
					t.Fatal("identity inputs")
				}
				if stage == "identity" {
					return "", "", sentinel
				}
				return "acme/app", "git@github.com:acme/app.git", nil
			}, Fleet: func(string, string, []string) ([]deps.Repository, error) {
				if stage == "fleet" {
					return nil, sentinel
				}
				return nil, nil
			}}
			_, err := New(d).Select(ctx, request)
			if err == nil {
				t.Fatal("missing refusal")
			}
			if stage == "abs" || stage == "identity" || stage == "fleet" {
				if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
			}
		})
	}
	var events []progress.Event
	request := Selection{ProjectsRoot: "root", Filter: "app", ExtraOrgs: []string{"acme"}, Fleet: true, Match: "acme/*", Parallel: 2, Progress: func(event progress.Event) { events = append(events, event) }}
	entries := []deps.Repository{{Slug: "acme/zapp", Path: "z", CloneURL: "z-url", Archived: true}, {Slug: "other/app"}, {Slug: "acme/app", Path: "a", CloneURL: "a-url"}}
	got, err := New(Dependencies{Fleet: func(root, filter string, extra []string) ([]deps.Repository, error) {
		if root != "root" || filter != "app" || !reflect.DeepEqual(extra, request.ExtraOrgs) {
			t.Fatal("fleet inputs")
		}
		return entries, nil
	}}).Select(ctx, request)
	if err != nil || len(got) != 2 || got[0].Slug != "acme/app" || !got[1].Archived || got[1].CloneURL != "z-url" || len(events) != 2 || events[1].Completed != 2 {
		t.Fatalf("selection=%+v events=%+v err=%v", got, events, err)
	}
	var localEvents []progress.Event
	got, err = New(Dependencies{Abs: func(path string) (string, error) {
		if path != "." {
			t.Fatalf("default path=%q", path)
		}
		return "/root/acme/app", nil
	}, Identity: func(string, string) (string, string, error) { return "acme/app", "origin", nil }}).Select(ctx, Selection{ProjectsRoot: "root", Parallel: 1, Progress: func(e progress.Event) { localEvents = append(localEvents, e) }})
	if err != nil || len(got) != 1 || got[0].CloneURL != "origin" || len(localEvents) != 2 || localEvents[1].Repository != "acme/app" {
		t.Fatalf("local=%+v %v", got, err)
	}
}

type inspectionContextKey struct{}

func TestInspectionStagesFinishBeforePersistenceAndRetainErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stage refused")
	ctx := context.WithValue(context.Background(), inspectionContextKey{}, "current")
	for _, verb := range []string{"graph", "drift"} {
		for _, stage := range []string{"engine", "home", "write", "success", "explicit", "findings"} {
			t.Run(verb+"/"+stage, func(t *testing.T) {
				t.Parallel()
				var order []string
				finish := func(s string) { order = append(order, "finish:"+s) }
				d := Dependencies{EnsureRoot: func(root string) (string, error) {
					order = append(order, "home")
					if root != "root" {
						t.Fatal(root)
					}
					if stage == "home" {
						return "", sentinel
					}
					return "/owned", nil
				}, BuildGraph: func(c context.Context, _ []deps.Repository, o deps.GraphOptions) (deps.Graph, error) {
					if c != ctx || o.GitHubDir != "root" {
						t.Fatal("graph context/options")
					}
					order = append(order, "engine")
					if stage == "engine" {
						return deps.Graph{}, sentinel
					}
					return deps.Graph{BaseRef: "main"}, nil
				}, AnalyzeDrift: func(c context.Context, _ []deps.Repository, o deps.DriftOptions) (deps.DriftReport, error) {
					if c != ctx || o.GitHubDir != "root" {
						t.Fatal("drift context/options")
					}
					order = append(order, "engine")
					if stage == "engine" {
						return deps.DriftReport{}, sentinel
					}
					report := deps.DriftReport{BaseRef: "main"}
					if stage == "findings" {
						report.Summary.Error = 1
					}
					return report, nil
				}, WriteGraphReports: func(path string, g deps.Graph, _ deps.GraphView) (deps.GraphReportPaths, error) {
					order = append(order, "write")
					if g.BaseRef != "main" || path == "" {
						t.Fatal("graph receipt")
					}
					if stage == "write" {
						return deps.GraphReportPaths{}, sentinel
					}
					return deps.GraphReportPaths{HTML: "actual.html"}, nil
				}, WriteDriftReports: func(path string, g deps.DriftReport) error {
					order = append(order, "write")
					if g.BaseRef != "main" || path == "" {
						t.Fatal("drift receipt")
					}
					if stage == "write" {
						return sentinel
					}
					return nil
				}}
				dir := ""
				if stage == "explicit" {
					dir = "explicit"
				}
				var err error
				if verb == "graph" {
					result, e := New(d).Graph(ctx, GraphRequest{Options: deps.GraphOptions{GitHubDir: "root", Ecosystem: deps.EcosystemGo}, ReportDir: dir, Finish: finish})
					err = e
					if e == nil && result.Paths.HTML != "actual.html" {
						t.Fatal(result)
					}
				} else {
					_, err = New(d).Drift(ctx, DriftRequest{Options: deps.DriftOptions{GitHubDir: "root", FailOnDrift: true}, ReportDir: dir, Finish: finish})
				}
				if stage == "engine" || stage == "home" || stage == "write" {
					if !errors.Is(err, sentinel) {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if len(order) < 2 || !strings.HasPrefix(order[1], "finish:") {
					t.Fatalf("finish order=%v", order)
				}
				if stage == "explicit" && reflect.DeepEqual(order, []string{"engine", "finish:completed", "home", "write"}) {
					t.Fatal("explicit dir resolved home")
				}
			})
		}
	}
	_, err := New(Dependencies{InspectPeers: func(c context.Context, o deps.PeerOptions) (deps.PeerReport, error) {
		if c != ctx || o.Package != "@acme/core" {
			t.Fatal("peer request")
		}
		return deps.PeerReport{}, sentinel
	}}).Peers(ctx, deps.PeerOptions{Package: "@acme/core"})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestMutationCustodySeparatesRunPersistenceAndResumeAuthority(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("run refused")
	persist := errors.New("persistence refused")
	for _, stage := range []string{"run", "home", "write", "explicit", "empty"} {
		t.Run("set/"+stage, func(t *testing.T) {
			t.Parallel()
			var order []string
			d := Dependencies{RunSet: func(context.Context, deps.Target, []deps.Repository, deps.Options) (deps.Report, error) {
				order = append(order, "run")
				report := deps.Report{Operation: "operation"}
				if stage == "empty" {
					report.Operation = ""
				}
				if stage == "run" {
					return report, sentinel
				}
				return report, nil
			}, EnsureRoot: func(string) (string, error) {
				order = append(order, "home")
				if stage == "home" {
					return "", persist
				}
				return "/owned", nil
			}, WriteSetReports: func(string, deps.Report) error {
				order = append(order, "write")
				if stage == "write" {
					return persist
				}
				return nil
			}}
			options := deps.Options{}
			if stage == "explicit" {
				options.ReportDir = "explicit"
			}
			result, err := New(d).Set(context.Background(), SetRequest{Options: options, Finish: func(s string) { order = append(order, s) }})
			if stage == "home" || stage == "write" {
				if !errors.Is(err, persist) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if stage == "run" && !errors.Is(result.RunError, sentinel) {
				t.Fatal(result)
			}
			if len(order) < 2 || order[0] != "run" {
				t.Fatal(order)
			}
			if stage == "empty" && len(order) != 2 {
				t.Fatal(order)
			}
		})
	}
	for _, stage := range []string{"home", "load-missing", "load-other", "parallel-invalid", "parallel-explicit", "parallel-retained", "run", "checkpoint", "final-write", "empty", "success"} {
		t.Run("bump/"+stage, func(t *testing.T) {
			t.Parallel()
			var writes int
			var finishStates []string
			d := Dependencies{EnsureRoot: func(string) (string, error) {
				if stage == "home" {
					return "", persist
				}
				return "/owned", nil
			}, LoadBumpReport: func(string) (deps.BumpReport, error) {
				if stage == "load-missing" {
					return deps.BumpReport{}, os.ErrNotExist
				}
				if stage == "load-other" {
					return deps.BumpReport{}, persist
				}
				pool := 3
				if stage == "parallel-invalid" {
					pool = 0
				}
				return deps.BumpReport{Parallel: pool, ParallelExplicit: true}, nil
			}, WriteBumpReports: func(string, deps.BumpReport) error {
				writes++
				if stage == "checkpoint" || stage == "final-write" && writes == 2 {
					return persist
				}
				return nil
			}, RunBump: func(_ context.Context, _ []deps.ReleaseEvent, _ []deps.Repository, o deps.BumpOptions) (deps.BumpReport, error) {
				if strings.HasPrefix(stage, "parallel-") {
					want := 3
					if stage == "parallel-explicit" {
						want = 8
					}
					if o.Parallel != want || o.Previous == nil || o.Previous.ParallelExplicit != true || o.ParallelExplicit != (stage != "parallel-explicit") {
						t.Fatalf("resume options=%+v", o)
					}
				}
				report := deps.BumpReport{Operation: "actual-operation"}
				if stage == "empty" {
					report.Operation = ""
					return report, sentinel
				}
				if stage == "run" {
					return report, sentinel
				}
				if err := o.Persist(report); err != nil {
					return report, err
				}
				return report, nil
			}}
			options := deps.BumpOptions{Options: deps.Options{GitHubDir: "root", Parallel: 8}, Ecosystem: deps.EcosystemGo}
			options.Resume = strings.HasPrefix(stage, "load-") || strings.HasPrefix(stage, "parallel-")
			request := BumpRequest{ProjectsRoot: "home-root", Resume: options.Resume, Options: options, ResumeParallelExplicit: stage == "parallel-explicit", Finish: func(s string) { finishStates = append(finishStates, s) }}
			result, err := New(d).Bump(context.Background(), request)
			switch stage {
			case "home", "load-other", "checkpoint", "final-write":
				if !errors.Is(err, persist) {
					t.Fatalf("%s err=%v", stage, err)
				}
			case "load-missing":
				if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "--resume requires") {
					t.Fatal(err)
				}
			case "parallel-invalid":
				if err == nil || !strings.Contains(err.Error(), "invalid parallelism") {
					t.Fatal(err)
				}
			case "run", "empty":
				if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if stage == "empty" && (writes != 0 || result.Report.Operation != "") {
				t.Fatalf("empty persistence=%d %+v", writes, result)
			}
			if len(finishStates) != 1 {
				t.Fatalf("finish must occur once at every boundary: %s %v", stage, finishStates)
			}
			if stage == "success" && (writes != 2 || !reflect.DeepEqual(finishStates, []string{"completed"})) {
				t.Fatalf("custody writes=%d finish=%v", writes, finishStates)
			}
		})
	}
}

func TestSeedKeepsRegistryAndExplicitProvenance(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("registry refused")
	for _, test := range []struct {
		name    string
		latest  bool
		changed []string
		failure bool
	}{
		{"empty", false, nil, false}, {"invalid", false, []string{"bad"}, false}, {"explicit", false, []string{"github.com/acme/lib@v1.2.3"}, false}, {"latest-invalid", true, []string{"bad"}, false}, {"latest", true, nil, false}, {"combined", true, []string{"github.com/acme/lib@v1.2.3"}, false}, {"registry", true, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			d := Dependencies{DeriveLatest: func(context.Context, []deps.Repository, []string, deps.BumpOptions) ([]deps.ReleaseEvent, []deps.LatestScopeResolution, error) {
				calls++
				if test.failure {
					return nil, nil, sentinel
				}
				return []deps.ReleaseEvent{{Dependency: "github.com/acme/lib", Version: "v1.3.0", Source: "registry"}}, []deps.LatestScopeResolution{{}}, nil
			}}
			result, err := New(d).Seed(context.Background(), SeedRequest{Ecosystem: deps.EcosystemGo, Latest: test.latest, Changed: test.changed})
			if test.name == "empty" || test.name == "invalid" || test.name == "latest-invalid" {
				if err == nil || calls != 0 {
					t.Fatalf("early refusal=%v calls%d", err, calls)
				}
			} else if test.failure {
				if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
			} else if err != nil || len(result.Events) != 1 {
				t.Fatalf("seed=%+v %v", result, err)
			}
			if test.name == "combined" && (result.Events[0].Version != "v1.3.0" || len(result.Resolutions) != 1) {
				t.Fatal(result)
			}
		})
	}
}

func TestDefaultBindingsUseActualEmptyOperationsAndCanonicalHome(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d := DefaultDependencies(os.Stderr)
	service := New(d)
	home, err := d.EnsureRoot(root)
	if err != nil || home != filepath.Join(root, ".wb") {
		t.Fatalf("actual home=%q %v", home, err)
	}
	result, err := service.Bump(context.Background(), BumpRequest{ProjectsRoot: root, Options: deps.BumpOptions{Options: deps.Options{GitHubDir: root}, Ecosystem: deps.EcosystemGo}})
	if err == nil || result.Report.Operation != "" || !strings.Contains(err.Error(), "at least one --changed") {
		t.Fatalf("actual empty engine=%+v %v", result, err)
	}
}

func TestBumpKeepsHomeAndEngineRootsDistinct(t *testing.T) {
	t.Parallel()
	var home, engine string
	service := New(Dependencies{EnsureRoot: func(root string) (string, error) { home = root; return "/home", nil }, RunBump: func(_ context.Context, _ []deps.ReleaseEvent, _ []deps.Repository, options deps.BumpOptions) (deps.BumpReport, error) {
		engine = options.GitHubDir
		return deps.BumpReport{}, nil
	}})
	result, err := service.Bump(context.Background(), BumpRequest{ProjectsRoot: "report-home-root", Options: deps.BumpOptions{Ecosystem: deps.EcosystemGo, Options: deps.Options{GitHubDir: "engine-root"}}})
	if err != nil || home != "report-home-root" || engine != "engine-root" || !strings.HasPrefix(result.ReportDir, "/home/reports/") {
		t.Fatalf("home=%q engine=%q result=%+v err=%v", home, engine, result, err)
	}
}

func TestBumpKeepsCheckpointAndEngineInputsDistinct(t *testing.T) {
	t.Parallel()
	var loaded string
	service := New(Dependencies{LoadBumpReport: func(path string) (deps.BumpReport, error) { loaded = path; return deps.BumpReport{Parallel: 2}, nil }, WriteBumpReports: func(path string, _ deps.BumpReport) error {
		if path != "checkpoint-root" {
			t.Fatalf("persistence path=%q", path)
		}
		return nil
	}, RunBump: func(_ context.Context, _ []deps.ReleaseEvent, _ []deps.Repository, options deps.BumpOptions) (deps.BumpReport, error) {
		if options.Resume || options.ReportDir != "engine-report" || options.Previous == nil {
			t.Fatalf("engine inputs overwritten: %+v", options)
		}
		return deps.BumpReport{Operation: "actual"}, nil
	}})
	result, err := service.Bump(context.Background(), BumpRequest{ReportDir: "checkpoint-root", Resume: true, Options: deps.BumpOptions{Ecosystem: deps.EcosystemGo, Options: deps.Options{ReportDir: "engine-report", Resume: false}}})
	if err != nil || loaded != "checkpoint-root" || result.ReportDir != "checkpoint-root" {
		t.Fatalf("result=%+v loaded=%q err=%v", result, loaded, err)
	}
}
