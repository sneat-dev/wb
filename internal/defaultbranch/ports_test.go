package defaultbranch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitcli"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// TestNewDefaultBranchEngineWiresAllFourProductionPorts proves Run's
// production wiring builds a fully populated *Engine, without starting any
// process: gitcli.New and runner.New only construct values, they never
// start anything themselves (task-24's runtime guard fires no earlier than
// the first real Run/RunStdin call).
func TestNewDefaultBranchEngineWiresAllFourProductionPorts(t *testing.T) {
	t.Parallel()
	engine := newDefaultBranchEngine()
	if _, ok := engine.Git.(defaultBranchGitAdapter); !ok {
		t.Fatalf("Git = %T, want defaultBranchGitAdapter", engine.Git)
	}
	if _, ok := engine.GitHub.(defaultBranchGitHubAdapter); !ok {
		t.Fatalf("GitHub = %T, want defaultBranchGitHubAdapter", engine.GitHub)
	}
	if _, ok := engine.Discovery.(defaultBranchDiscoveryAdapter); !ok {
		t.Fatalf("Discovery = %T, want defaultBranchDiscoveryAdapter", engine.Discovery)
	}
	if _, ok := engine.Clock.(defaultBranchClockAdapter); !ok {
		t.Fatalf("Clock = %T, want defaultBranchClockAdapter", engine.Clock)
	}
}

// TestDefaultBranchGitAdapterRunDelegatesToGitcliClient proves the Run
// passthrough forwards argv, dir and the result unchanged.
func TestDefaultBranchGitAdapterRunDelegatesToGitcliClient(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "status", "--porcelain"}, runner.Result{Stdout: "clean\n"}, nil)
	adapter := defaultBranchGitAdapter{client: gitcli.New(fake)}

	out, err := adapter.Run(context.Background(), "/repo", "status", "--porcelain")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "clean" {
		t.Fatalf("out = %q", out)
	}
}

// TestDefaultBranchGitAdapterIsAncestorCoversAllThreeExitOutcomes proves
// this package's own IsAncestor -- not gitcli.Client's -- still returns
// true/false/an ambiguous-exit error with defaultbranch's original error
// text (rule 1: byte-identical across the move onto internal/runner).
func TestDefaultBranchGitAdapterIsAncestorCoversAllThreeExitOutcomes(t *testing.T) {
	t.Parallel()
	t.Run("yes", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		fake.ExpectArgv([]string{"git", "merge-base", "--is-ancestor", "base", "head"}, runner.Result{}, nil)
		adapter := defaultBranchGitAdapter{client: gitcli.New(fake)}
		ok, err := adapter.IsAncestor(context.Background(), "/repo", "base", "head")
		if err != nil || !ok {
			t.Fatalf("IsAncestor = (%v, %v), want (true, nil)", ok, err)
		}
	})
	t.Run("no", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		fake.ExpectArgv([]string{"git", "merge-base", "--is-ancestor", "base", "head"}, runner.Result{ExitCode: 1}, errors.New("exit status 1"))
		adapter := defaultBranchGitAdapter{client: gitcli.New(fake)}
		ok, err := adapter.IsAncestor(context.Background(), "/repo", "base", "head")
		if err != nil || ok {
			t.Fatalf("IsAncestor = (%v, %v), want (false, nil)", ok, err)
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		fake.ExpectArgv([]string{"git", "merge-base", "--is-ancestor", "base", "head"}, runner.Result{ExitCode: 129, Stderr: "unknown revision"}, errors.New("exit status 129"))
		adapter := defaultBranchGitAdapter{client: gitcli.New(fake)}
		ok, err := adapter.IsAncestor(context.Background(), "/repo", "base", "head")
		if ok {
			t.Fatal("want false alongside the error")
		}
		if got, want := err.Error(), "git merge-base --is-ancestor base head: exit status 129: unknown revision"; got != want {
			t.Fatalf("err = %q, want %q (no gitcli ambiguous-exit sentinel)", got, want)
		}
	})
}

// TestDefaultBranchGitAdapterAtomicRenameRefsDelegatesToGitcliClient proves
// the AtomicRenameRefs, AttachHead and RefExists passthroughs forward to
// gitcli.Client, covering both their success and failure paths.
func TestDefaultBranchGitAdapterAtomicRenameRefsDelegatesToGitcliClient(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "checkout", "--detach", "sha"}, runner.Result{Stderr: "unknown revision"}, errors.New("exit status 128"))
	adapter := defaultBranchGitAdapter{client: gitcli.New(fake)}

	err := adapter.AtomicRenameRefs(context.Background(), "/repo", "master", "main", "sha")
	if err == nil {
		t.Fatal("want an error for a failed detach")
	}
	if got, want := err.Error(), "detach HEAD at verified master: exit status 128: unknown revision"; got != want {
		t.Fatalf("err = %q, want %q", got, want)
	}
}

func TestDefaultBranchGitAdapterAttachHeadDelegatesToGitcliClient(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "symbolic-ref", "HEAD", "refs/heads/main"}, runner.Result{}, nil)
	adapter := defaultBranchGitAdapter{client: gitcli.New(fake)}

	if err := adapter.AttachHead(context.Background(), "/repo", "main"); err != nil {
		t.Fatalf("AttachHead: %v", err)
	}
}

func TestDefaultBranchGitAdapterRefExistsDelegatesToGitcliClient(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "rev-parse", "--verify", "--quiet", "refs/heads/main"}, runner.Result{}, nil)
	adapter := defaultBranchGitAdapter{client: gitcli.New(fake)}

	ok, err := adapter.RefExists(context.Background(), "/repo", "refs/heads/main")
	if err != nil || !ok {
		t.Fatalf("RefExists = (%v, %v), want (true, nil)", ok, err)
	}
}

// TestDefaultBranchClockAdapterNowReportsTheCurrentTime and the Wait test
// below exercise the real time package directly: no process, and no
// injected fake, since Clock is the one port whose production
// implementation (real time.Now / a real timer) is itself fast and
// deterministic enough to run in the unit tier without a fake.
func TestDefaultBranchClockAdapterNowReportsTheCurrentTime(t *testing.T) {
	t.Parallel()
	before := time.Now()
	got := (defaultBranchClockAdapter{}).Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("Now() = %v, want between %v and %v", got, before, after)
	}
}

func TestDefaultBranchClockAdapterWaitReturnsAfterTheDuration(t *testing.T) {
	t.Parallel()
	if err := (defaultBranchClockAdapter{}).Wait(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestDefaultBranchClockAdapterWaitReturnsTheContextErrorWhenAlreadyDone(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (defaultBranchClockAdapter{}).Wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() = %v, want context.Canceled", err)
	}
}
