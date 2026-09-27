package worktrees

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCreateRefusesCanonicalGitFailuresBeforePublication(t *testing.T) {
	cases := []struct {
		name  string
		match func([]string) bool
	}{
		{"canonical root query", func(args []string) bool {
			return len(args) == 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel"
		}},
		{"canonical Git directory query", func(args []string) bool {
			return len(args) == 2 && args[0] == "rev-parse" && args[1] == "--absolute-git-dir"
		}},
		{"canonical common directory query", func(args []string) bool {
			return len(args) == 3 && args[0] == "rev-parse" && args[1] == "--path-format=absolute" && args[2] == "--git-common-dir"
		}},
		{"fetch verified base", func(args []string) bool {
			return len(args) > 0 && args[0] == "fetch"
		}},
		{"resolve fetched base", func(args []string) bool {
			return len(args) == 3 && args[0] == "rev-parse" && args[1] == "--verify" &&
				strings.HasPrefix(args[2], "refs/wb/fetch-base/")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			configureFixtureSharedWorktrees(t, fixture)
			injected := errors.New("canonical Git call failed")
			seen := false
			ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, runSecure func() ([]byte, error)) ([]byte, error) {
				if !seen && tc.match(args) {
					seen = true
					return nil, injected
				}
				return runSecure()
			})
			_, err := Create(ctx, []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot,
				Operation:    "canonical-git-failure",
				WorkLog:      WorkLogOptions{Model: "unknown"},
			})
			if !seen || !errors.Is(err, injected) {
				t.Fatalf("Create with injected %s failure = %v; intercepted=%t", tc.name, err, seen)
			}
			assertFailedCreateRolledBack(t, fixture, "canonical-git-failure")
		})
	}
}

func TestCanonicalGitInterceptorRechecksDescriptorsAfterInjectedCall(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, _ []string, _ func() ([]byte, error)) ([]byte, error) {
		if err := os.Rename(fixture.canonical, fixture.canonical+"-moved"); err != nil {
			t.Fatal(err)
		}
		return []byte("false success"), nil
	})
	if _, err := gitCanonical(ctx, canonical, "rev-parse", "HEAD"); err == nil ||
		!strings.Contains(err.Error(), "canonical repository path changed") {
		t.Fatalf("injected call after canonical path move = %v, want descriptor validation refusal", err)
	}
}

func TestCanonicalGitInterceptorCannotBypassInitialAuthorization(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	if err := os.Rename(fixture.canonical, fixture.canonical+"-moved"); err != nil {
		t.Fatal(err)
	}
	called := false
	ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, _ []string, _ func() ([]byte, error)) ([]byte, error) {
		called = true
		return []byte("false success"), nil
	})
	if _, err := gitCanonical(ctx, canonical, "rev-parse", "HEAD"); err == nil ||
		!strings.Contains(err.Error(), "canonical repository path changed") || called {
		t.Fatalf("injected call before canonical authorization = (%v, called=%t), want path refusal without interception", err, called)
	}
}
