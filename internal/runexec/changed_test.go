package runexec

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/quality"
	"testing"
)

func TestChangedFailsClosedOnDirectoryResolutionError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("directory removed")
	ops := ChangedOperations{Getwd: func() (string, error) { return "", sentinel }}
	if _, err := ops.Run(context.Background(), ChangedRequest{Argv: []string{"go", "test"}, Target: "main"}); err != sentinel {
		t.Fatalf("error=%v", err)
	}
}
func TestChangedResolvesTargetAndCopiesArguments(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), testContextKey{}, true)
	args := []string{"go", "test"}
	packages := []string{"./app"}
	ops := ChangedOperations{Getwd: func() (string, error) { return "/repo", nil }, DefaultBranch: func(root string) string {
		if root != "/repo" {
			t.Fatal(root)
		}
		return "main"
	}, Packages: func(got context.Context, root, target string) (quality.ChangedPackagesResult, error) {
		if got != ctx || root != "/repo" || target != "main" {
			t.Fatal("request mismatch")
		}
		return quality.ChangedPackagesResult{Packages: packages, Target: "main", MergeBase: "abc"}, nil
	}}
	result, err := ops.Run(ctx, ChangedRequest{Argv: args})
	if err != nil || result.Target != "main" || result.MergeBase != "abc" || len(result.Argv) != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result.Argv[0] = "changed"
	result.Packages[0] = "changed"
	if args[0] != "go" || packages[0] != "./app" {
		t.Fatal("inputs mutated")
	}
	ops.DefaultBranch = func(string) string { return " " }
	result, err = ops.Run(ctx, ChangedRequest{})
	if err != nil || !result.MissingTarget {
		t.Fatalf("missing target=%+v %v", result, err)
	}
	sentinel := errors.New("packages failed")
	ops.Packages = func(context.Context, string, string) (quality.ChangedPackagesResult, error) {
		return quality.ChangedPackagesResult{}, sentinel
	}
	if _, err := ops.Run(ctx, ChangedRequest{Target: "base"}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	ops.Packages = func(context.Context, string, string) (quality.ChangedPackagesResult, error) {
		return quality.ChangedPackagesResult{Target: "base", MergeBase: "def"}, nil
	}
	result, err = ops.Run(ctx, ChangedRequest{Target: "base"})
	if err != nil || !result.NoWork {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
