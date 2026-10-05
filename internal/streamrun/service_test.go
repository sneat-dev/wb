package streamrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func isolatedService(t *testing.T) *Service {
	t.Helper()
	service := New(Config{Machine: func(string) (string, error) { return "machine", nil }, Login: func() (string, error) { return "login", nil }, Executable: func() string { return "/verified/wb" }})
	service.registered = func() string { return "session" }
	return service
}
func TestStartGraphFindingFollowsSuccessfulCreationAndExistingFindings(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("operation failed")
	existing := streams.PreflightFinding{Check: "original", Status: streams.PreflightUnknown, Detail: "retained"}
	request := Creation{ProjectsRoot: "/chosen", Base: "release", SessionRequired: true, WorkLog: worktrees.WorkLogOptions{Model: "precise"}}
	options := streams.StartOptions{Name: "batch", Repositories: []string{"acme/lib"}, Library: "acme/lib", Base: "release"}
	for _, test := range []struct {
		name                        string
		openErr, graphErr, startErr error
		proposed                    bool
		wantCalls                   string
	}{{name: "open failure", openErr: wantErr, wantCalls: "open"}, {name: "graph failure", graphErr: wantErr, wantCalls: "open,identity,graph"}, {name: "start failure", startErr: wantErr, wantCalls: "open,identity,graph,start"}, {name: "valid evidence", proposed: true, wantCalls: "open,identity,graph,start"}, {name: "missing evidence", wantCalls: "open,identity,graph,start"}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var calls []string
			service := isolatedService(t)
			store := streams.OpenAt("/unused")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			service.open = func(root string) (*streams.Store, error) {
				calls = append(calls, "open")
				if root != request.ProjectsRoot {
					t.Fatal(root)
				}
				return store, test.openErr
			}
			service.config.Machine = func(root string) (string, error) { calls = append(calls, "identity"); return "machine", nil }
			service.graph = func(root string, repos []string) ([]string, bool, error) {
				calls = append(calls, "graph")
				if root != request.ProjectsRoot || !reflect.DeepEqual(repos, options.Repositories) {
					t.Fatalf("graph=%s %v", root, repos)
				}
				return []string{"acme/app"}, test.proposed, test.graphErr
			}
			service.start = func(engine *streams.Engine, gotCtx context.Context, got streams.StartOptions, transitive []string) (streams.StartResult, error) {
				calls = append(calls, "start")
				if gotCtx != ctx || !reflect.DeepEqual(got, options) || !reflect.DeepEqual(transitive, []string{"acme/app"}) {
					t.Fatalf("start=%v %v %v", gotCtx, got, transitive)
				}
				adapter := engine.Worktrees.(*streamWorktrees)
				if engine.Store != store || engine.ProjectsRoot != "/chosen" || engine.Login != "login" || engine.Machine != "machine" || engine.Session != "session" || adapter.base != "release" || !adapter.sessionMode || adapter.workLog.Model != "precise" || adapter.create == nil || adapter.cleanup == nil || engine.HooksCheck == nil {
					t.Fatalf("engine=%+v adapter=%+v", engine, adapter)
				}
				return streams.StartResult{Reported: []streams.PreflightFinding{existing}}, test.startErr
			}
			result, err := service.Start(ctx, request, options)
			if strings.Join(calls, ",") != test.wantCalls {
				t.Fatal(calls)
			}
			if test.openErr != nil || test.graphErr != nil || test.startErr != nil {
				if !errors.Is(err, wantErr) {
					t.Fatal(err)
				}
				if len(result.Reported) > 1 {
					t.Fatal(result.Reported)
				}
				return
			}
			if err != nil || result.Reported[0] != existing {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if test.proposed {
				if len(result.Reported) != 1 {
					t.Fatal(result.Reported)
				}
			} else {
				want := streams.PreflightFinding{Check: "transitive-membership", Status: streams.PreflightUnknown, Detail: "no dependency graph evidence on this machine; run `wb deps graph --fleet --format json` so a transitive consumer left out of the stream is named"}
				if len(result.Reported) != 2 || result.Reported[1] != want {
					t.Fatal(result.Reported)
				}
			}
		})
	}
}
func TestServiceOperationsPreserveContextInputsAndOpeningErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("open failed")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := Creation{ProjectsRoot: "/chosen", Base: "base"}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "delegation", true: "opening failure"}[fail], func(t *testing.T) {
			t.Parallel()
			service := isolatedService(t)
			store := streams.OpenAt(t.TempDir())
			service.open = func(root string) (*streams.Store, error) {
				if root != "/chosen" {
					t.Fatal(root)
				}
				if fail {
					return nil, sentinel
				}
				return store, nil
			}
			calls := 0
			service.join = func(_ *streams.Engine, c context.Context, o streams.JoinOptions) (streams.StartResult, error) {
				calls++
				if c != ctx || o.Name != "join" || o.Role != streams.RoleLibrary {
					t.Fatal(o)
				}
				return streams.StartResult{}, sentinel
			}
			service.status = func(_ *streams.Engine, c context.Context, name string) (streams.Status, error) {
				calls++
				if c != ctx || name != "status" {
					t.Fatal(name)
				}
				return streams.Status{}, sentinel
			}
			service.end = func(_ *streams.Engine, c context.Context, o streams.EndOptions) (streams.EndResult, error) {
				calls++
				if c != ctx || o.Name != "end" || !o.Apply {
					t.Fatal(o)
				}
				return streams.EndResult{}, sentinel
			}
			if _, err := service.Join(ctx, request, streams.JoinOptions{Name: "join", Role: streams.RoleLibrary}); !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
			if _, err := service.Status(ctx, "/chosen", "status"); !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
			if _, err := service.End(ctx, "/chosen", streams.EndOptions{Name: "end", Apply: true}); !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
			if fail {
				if calls != 0 {
					t.Fatal(calls)
				}
				if _, _, err := service.List(ctx, "/chosen"); !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
				if err := service.Delete("/chosen", "absent"); !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
			} else {
				if calls != 3 {
					t.Fatal(calls)
				}
				if _, _, err := service.List(ctx, "/chosen"); err != nil {
					t.Fatal(err)
				}
				if err := service.Delete("/chosen", "absent"); err == nil {
					t.Fatal("missing record accepted")
				}
			}
		})
	}
}
func TestLeaseIdentityFallbackKeepsMachineAndDoesNotResolveLoginWithoutConfig(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                   string
		machineErr, loginErr   error
		wantLogin, wantMachine string
		wantCalls              int
	}{{name: "configured", wantLogin: "login", wantMachine: "machine", wantCalls: 1}, {name: "missing configuration", machineErr: errors.New("config"), wantCalls: 0}, {name: "failed login", loginErr: errors.New("login"), wantMachine: "machine", wantCalls: 1}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := isolatedService(t)
			calls := 0
			service.config.Machine = func(string) (string, error) { return "machine", test.machineErr }
			service.config.Login = func() (string, error) { calls++; return "login", test.loginErr }
			login, machine := service.Identity("/chosen")
			if login != test.wantLogin || machine != test.wantMachine || calls != test.wantCalls {
				t.Fatalf("identity=%s/%s calls%d", login, machine, calls)
			}
		})
	}
}

//nolint:paralleltest // This contract replaces the process-wide registered-session resolver; preserve sequential install/query/restore.
func TestSessionIdentityUsesOnlyRegisteredSession(t *testing.T) {
	t.Cleanup(func() { worktrees.SetSessionResolver(nil) })
	worktrees.SetSessionResolver(nil)
	if got := SessionIdentity(); got != "" {
		t.Fatal(got)
	}
	worktrees.SetSessionResolver(func() (worktrees.AgentIdentity, bool) {
		return worktrees.AgentIdentity{WBSessionID: "registered", Registered: true}, true
	})
	if got := SessionIdentity(); got != "registered" {
		t.Fatal(got)
	}
}
func TestListSharesOnlyFrozenReadOnlySourceAcrossChildren(t *testing.T) {
	t.Parallel()
	store := streams.OpenAt(t.TempDir())
	for _, name := range []string{"first", "second"} {
		if _, err := store.Create(streams.Stream{Name: name, Members: []streams.Member{{Repository: "acme/app"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(store.Dir("broken"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Dir("broken"), "stream.json")
	if err := os.WriteFile(path, []byte("invalid json"), 0644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first read", "second read", "third read"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			service := isolatedService(t)
			service.open = func(string) (*streams.Store, error) { return store, nil }
			all, unreadable, err := service.List(context.Background(), "/chosen")
			if err != nil || len(all) != 2 || len(unreadable) != 1 || unreadable[0].Name != "broken" {
				t.Fatalf("list=%+v unreadable=%+v err=%v", all, unreadable, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("source mutated", err)
			}
		})
	}
}
