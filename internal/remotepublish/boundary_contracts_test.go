package remotepublish

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestPublicationStagesPreserveErrorsAndDetachedCollection(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"open", "login error", "login empty", "select", "worktrees", "success", "dry"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			config := cockpitConfigFile(t, "remote:\n  provider: git\n  repo: acme/wb-state\n  machine: laptop\n")()
			sentinel := errors.New("selected stage refusal")
			deps := DefaultDependencies(config, testExitError)
			deps.Login = func() (string, error) {
				if stage == "login error" {
					return "", sentinel
				}
				if stage == "login empty" {
					return "", nil
				}
				return "alice", nil
			}
			deps.Open = func(remotestate.Config, string) (remotestate.Provider, error) {
				if stage == "open" {
					return nil, sentinel
				}
				return &capturingProvider{}, nil
			}
			reads := 0
			deps.ReadRepository = func(path string) (gitops.RepoStatus, gitops.TrackingState, error) {
				reads++
				return gitops.RepoStatus{}, gitops.TrackingState{Branch: "main"}, nil
			}
			deps.Select = func(r reposelection.Request) ([]reposelection.Target, error) {
				if r.ProjectsRoot != "private-root" || r.Filter != "acme" || r.Parallel != 2 || !r.Fleet || r.AllowEmpty {
					t.Errorf("selection=%+v", r)
				}
				if stage == "select" {
					return nil, sentinel
				}
				return []reposelection.Target{{Repository: "acme/repo", Path: "private/repo"}}, nil
			}
			listed := false
			deps.ListWorktrees = func(ctx context.Context, opts worktrees.ListOptions) ([]worktrees.ListResult, error) {
				listed = true
				if ctx.Err() != nil || opts.ProjectsRoot != "private-root" || opts.Filter != "acme" || opts.OwnerState != "" {
					t.Errorf("worktree request=%+v ctx=%v", opts, ctx.Err())
				}
				opts.Progress(worktrees.ListProgress{Done: true})
				if stage == "worktrees" {
					return nil, sentinel
				}
				return nil, nil
			}
			var events []string
			progress := Progress{Start: func(n int) {
				if n != 1 {
					t.Errorf("total=%d", n)
				}
				events = append(events, "start")
			}, RepositoryComplete: func(s string, e error) {
				if s != "acme/repo" || e != nil {
					t.Errorf("completed=%s %v", s, e)
				}
				events = append(events, "repository")
			}, Phase: func(s string) { events = append(events, s) }, Worktree: func(e worktrees.ListProgress) {
				if !e.Done {
					t.Error("worktree callback lost")
				}
			}, Finish: func(s string) { events = append(events, s) }, Fail: func(e error) {
				if !errors.Is(e, sentinel) {
					t.Errorf("fail=%v", e)
				}
				events = append(events, "fail")
			}}
			result, err := New(deps).Publish(Request{ProjectsRoot: "private-root", Filter: "acme", Parallel: 2, DryRun: stage == "dry"}, progress, io.Discard)
			switch stage {
			case "success", "dry":
				if err != nil || reads != 1 || !listed || result.Report.Key != "alice/laptop" || result.DryRun != (stage == "dry") {
					t.Fatalf("result=%+v err=%v reads=%d listed=%v", result, err, reads, listed)
				}
			case "login empty", "login error":
				if err == nil || !strings.Contains(err.Error(), "gh auth status") {
					t.Fatalf("login refusal=%v", err)
				}
			default:
				if !errors.Is(err, sentinel) {
					t.Fatalf("stage error=%v", err)
				}
			}
		})
	}
}

func TestCollectorPropagatesReadAndListingBeforeBuilding(t *testing.T) {
	t.Parallel()
	deps := DefaultDependencies("", testExitError)
	sentinel := errors.New("listing refused")
	deps.Select = func(reposelection.Request) ([]reposelection.Target, error) { return nil, sentinel }
	if _, err := New(deps).collectSnapshot(t.Context(), "", "", 1, remotestate.Snapshot{}, remotestate.RedactNone, Progress{}, nil); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	deps.Select = func(reposelection.Request) ([]reposelection.Target, error) { return nil, nil }
	deps.ListWorktrees = func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) { return nil, sentinel }
	if _, err := New(deps).collectSnapshot(t.Context(), "", "", 1, remotestate.Snapshot{}, remotestate.RedactNone, Progress{}, nil); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestDefaultProviderBindingsAndPeriodicNativeReadFallback(t *testing.T) {
	t.Parallel()
	deps := DefaultDependencies("", testExitError)
	if deps.Version() == "" || deps.Now().Location() != time.UTC {
		t.Fatal("default metadata binding lost")
	}
	cfg := publishConfig(remotestate.PublishConfig{Interval: time.Hour})
	root := t.TempDir()
	if _, err := deps.Open(cfg, root); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(cfg, blocked, testExitError); err == nil {
		t.Fatal("invalid canonical path accepted")
	}
	deps.ReadRepository = nil
	deps.Open = func(remotestate.Config, string) (remotestate.Provider, error) { return &capturingProvider{}, nil }
	deps.Login = func() (string, error) { return "alice", nil }
	p := New(deps).Periodic(cfg, root, nil, nil)
	p.Publish(t.Context(), nil)
	if p.Status().Published != 1 {
		t.Fatalf("fallback empty scan=%+v", p.Status())
	}
}
