package depsrun

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type propagationHashGit struct {
	locallink.Git
	hash func(context.Context, string) (string, bool, error)
}

func (g propagationHashGit) ContentHash(ctx context.Context, path string) (string, bool, error) {
	return g.hash(ctx, path)
}
func TestPropagationSetupFailuresPreserveOrderAndIdentity(t *testing.T) {
	t.Parallel()
	boom := errors.New("setup refused")
	for _, stage := range []string{"store", "home", "hash"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			var order []string
			d := DefaultPropagationDependencies()
			d.OpenStore = func(root string) (*streams.Store, error) {
				order = append(order, "store")
				if root != "root" {
					t.Fatal(root)
				}
				if stage == "store" {
					return nil, boom
				}
				return nil, nil
			}
			d.Home = func(string) (string, error) {
				order = append(order, "home")
				if stage == "home" {
					return "", boom
				}
				return "home", nil
			}
			d.Git = func(time.Duration) locallink.Git {
				order = append(order, "git")
				return propagationHashGit{hash: func(context.Context, string) (string, bool, error) {
					order = append(order, "hash")
					return "", false, boom
				}}
			}
			d.Verifier = func(timeout time.Duration) locallink.Verifier {
				order = append(order, "verifier")
				return locallink.QualityVerifier{}
			}
			d.Node = func(string, string, time.Duration) locallink.Node {
				order = append(order, "node")
				return locallink.ExecNode{}
			}
			_, err := NewPropagation(d).Run(context.Background(), PropagationRequest{ProjectsRoot: "root", Options: locallink.Options{Library: "library"}})
			if !errors.Is(err, boom) {
				t.Fatalf("identity=%v", err)
			}
			want := map[string][]string{"store": {"store"}, "home": {"store", "home"}, "hash": {"store", "home", "git", "verifier", "node", "hash"}}[stage]
			if !reflect.DeepEqual(order, want) {
				t.Fatalf("order=%v want%v", order, want)
			}
		})
	}
}
func TestPropagationPrehashBindsNodeAndCallsActualEngine(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d := DefaultPropagationDependencies()
	var hashes []string
	var caches []string
	timeout := 7 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	git := d.Git
	d.Git = func(got time.Duration) locallink.Git {
		if got != timeout {
			t.Fatal(got)
		}
		return propagationHashGit{Git: git(got), hash: func(gotCtx context.Context, path string) (string, bool, error) {
			if gotCtx != ctx || path != "" {
				t.Fatal("context/library changed")
			}
			return "hash", true, nil
		}}
	}
	node := d.Node
	d.Node = func(cache, hash string, got time.Duration) locallink.Node {
		if got != timeout {
			t.Fatal(got)
		}
		hashes = append(hashes, hash)
		caches = append(caches, cache)
		return node(cache, hash, got)
	}
	_, err := NewPropagation(d).Run(ctx, PropagationRequest{ProjectsRoot: root, Options: locallink.Options{Timeout: timeout}})
	if err == nil || !strings.Contains(err.Error(), "library worktree is required") {
		t.Fatalf("actual engine error=%v", err)
	}
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "cache", "local-link")
	if !reflect.DeepEqual(hashes, []string{"", "hash"}) || !reflect.DeepEqual(caches, []string{want, want}) {
		t.Fatalf("node=%v %v", hashes, caches)
	}
}
func TestPropagationUndoUsesRealStoreWithoutLibraryOrPrehash(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d := DefaultPropagationDependencies()
	git := d.Git
	d.Git = func(timeout time.Duration) locallink.Git {
		return propagationHashGit{Git: git(timeout), hash: func(context.Context, string) (string, bool, error) {
			t.Fatal("undo hashed removed library")
			return "", false, nil
		}}
	}
	result, err := NewPropagation(d).Run(context.Background(), PropagationRequest{ProjectsRoot: root, Options: locallink.Options{Undo: true, Library: filepath.Join(root, "removed"), Consumers: []string{root}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Plan) != 3 || len(result.Consumers) != 1 {
		t.Fatalf("actual undo=%+v", result)
	}
	defaults := DefaultPropagationDependencies()
	if defaults.Git(3*time.Second).(locallink.ExecGit).Timeout != 3*time.Second || defaults.Node("cache", "hash", 4*time.Second).(locallink.ExecNode).ContentHash != "hash" || defaults.Verifier(5*time.Second).(locallink.QualityVerifier).Options.Timeout != 5*time.Second {
		t.Fatal("default ports changed")
	}
}
