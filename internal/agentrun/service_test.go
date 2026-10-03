package agentrun

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDispatchUsesActualDependencyBuilderAndPreservesRequests(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	deps := DefaultDependencies()
	sentinel := errors.New("config error")
	calls := []string{}
	now := time.Unix(100, 0)
	deps.Now = func() time.Time { return now }
	deps.ConfigPath = func() string { return "private-config" }
	deps.LoadConfig = func(path string) (agents.Config, error) {
		calls = append(calls, "config")
		if path != "private-config" {
			t.Fatal(path)
		}
		return agents.Config{}, sentinel
	}
	deps.BeforeCreate = func(r string, repos []string) error {
		calls = append(calls, "before")
		if r != root || !reflect.DeepEqual(repos, []string{"a/b"}) {
			t.Fatal(r, repos)
		}
		return sentinel
	}
	var warning bytes.Buffer
	results := []worktrees.CreateResult{{CanonicalDir: "canonical", WorktreeDir: "child"}}
	deps.AfterCreate = func(r string, w io.Writer, base string, got []worktrees.CreateResult) {
		calls = append(calls, "after")
		if r != root || w != &warning || base != "main" || !reflect.DeepEqual(got, results) {
			t.Fatal(r, base, got)
		}
	}
	deps.Executable = func() (string, error) { return "wb-private", nil }
	deps.SpawnOwner = func(dir string, executable func() (string, error)) (int, error) {
		calls = append(calls, "spawn")
		exe, err := executable()
		if dir != filepath.Join(physical, ".wb", "agents", "id") || exe != "wb-private" || err != nil {
			t.Fatal(dir, exe, err)
		}
		return 42, nil
	}
	ctx := context.WithValue(t.Context(), contextKey{}, "context")
	request := agents.DispatchRequest{Mode: agents.ModeNew, Worktree: "work", Profile: "cheap", Task: "private", Base: "main"}
	deps.Dispatch = func(c context.Context, r agents.DispatchRequest, d agents.DispatchDeps) (agents.Record, error) {
		if c != ctx || r != request || d.Home != filepath.Join(physical, ".wb") || d.ProjectsRoot != root || d.ConfigPath != "private-config" || d.Now() != now {
			t.Fatal(c, r, d)
		}
		if _, err := d.LoadConfig(); err != sentinel {
			t.Fatal(err)
		}
		if err := d.BeforeCreate([]string{"a/b"}); err != sentinel {
			t.Fatal(err)
		}
		d.AfterCreate(nil, results)
		if pid, err := d.SpawnOwner("id"); pid != 42 || err != nil {
			t.Fatal(pid, err)
		}
		return agents.Record{AgentID: "id", State: agents.StateCompleted}, nil
	}
	service := New(deps)
	result, err := service.Dispatch(ctx, DispatchRequest{root, &warning, request})
	if err != nil || result.AgentID != "id" || !result.Terminal || !reflect.DeepEqual(calls, []string{"config", "before", "after", "spawn"}) {
		t.Fatal(result, err, calls)
	}
	deps.Dispatch = func(context.Context, agents.DispatchRequest, agents.DispatchDeps) (agents.Record, error) {
		return agents.Record{}, sentinel
	}
	if _, err := New(deps).Dispatch(ctx, DispatchRequest{ProjectsRoot: root}); err != sentinel {
		t.Fatal(err)
	}
	deps.EnsureRoot = func(string) (string, error) { return "", sentinel }
	if _, err := New(deps).Dispatch(ctx, DispatchRequest{}); err != sentinel {
		t.Fatal(err)
	}
}
func TestReadOperationsUseAnImmutableTerminalCorpus(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".wb")
	store := agents.NewStore(home)
	id, err := agents.NewID()
	if err != nil {
		t.Fatal(err)
	}
	record := agents.Record{AgentID: id, State: agents.StateCompleted, StartedAt: time.Unix(100, 0), LogPath: store.LogPath(id), Task: "private task"}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record.LogPath, []byte("raw transcript\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deps := DefaultDependencies()
	service := New(deps)
	for _, verb := range []string{"status", "list", "await", "logs"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			ops := service.Operations()
			switch verb {
			case "status":
				result, err := ops.Status(t.Context(), LookupRequest{root, id})
				if err != nil || result.AgentID != id || !result.Terminal {
					t.Fatal(result, err)
				}
			case "list":
				results, err := ops.List(t.Context(), ListRequest{root})
				if err != nil || len(results) != 1 || results[0].AgentID != id {
					t.Fatal(results, err)
				}
			case "await":
				result, err := ops.Await(t.Context(), AwaitRequest{root, id, time.Hour})
				if err != nil || !result.Terminal {
					t.Fatal(result, err)
				}
			case "logs":
				text, err := ops.Logs(t.Context(), LogsRequest{root, id, 8, true})
				if err != nil || !strings.Contains(text, "raw transcript") {
					t.Fatal(text, err)
				}
			}
		})
	}
}
func TestReadAndStopFailuresPreserveIdentity(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("home refused")
	for _, verb := range []string{"status", "list", "await", "logs", "stop"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			deps := DefaultDependencies()
			deps.Root = func(string) (string, error) { return "", sentinel }
			s := New(deps)
			var err error
			switch verb {
			case "status":
				_, err = s.Status(t.Context(), LookupRequest{})
			case "list":
				_, err = s.List(t.Context(), ListRequest{})
			case "await":
				_, err = s.Await(t.Context(), AwaitRequest{})
			case "logs":
				_, err = s.Logs(t.Context(), LogsRequest{})
			case "stop":
				_, err = s.Stop(t.Context(), LookupRequest{})
			}
			if err != sentinel {
				t.Fatal(err)
			}
		})
	}
	deps := DefaultDependencies()
	root := t.TempDir()
	s := New(deps)
	if _, err := s.Status(t.Context(), LookupRequest{root, "invalid"}); err == nil {
		t.Fatal("missing load refusal")
	}
	if _, err := s.Logs(t.Context(), LogsRequest{ProjectsRoot: root, AgentID: "invalid"}); err == nil {
		t.Fatal("missing logs refusal")
	}
	deps.Stop = func(store agents.Store, id string, _ agents.OwnerDeps) (agents.Record, error) {
		if filepath.Base(store.Root) != "agents" || filepath.Base(filepath.Dir(store.Root)) != ".wb" || id != "id" {
			t.Fatal(store, id)
		}
		return agents.Record{AgentID: id}, sentinel
	}
	if _, err := New(deps).Stop(t.Context(), LookupRequest{root, "id"}); err != sentinel {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".wb"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".wb", "agents"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(t.Context(), ListRequest{root}); err == nil {
		t.Fatal("expected actual non-directory list error")
	}
}
func TestDefaultWaitCompletesOrCancels(t *testing.T) {
	t.Parallel()
	if err := wait(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type contextKey struct{}
