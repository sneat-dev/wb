package cmddisk

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestDependencyCacheKeepsPlanningDefaultAndForwardsExplicitPolicy(t *testing.T) {
	t.Parallel()
	for index, flags := range [][]string{nil, {"--apply=false"}, {"--apply", "--image", "sha256:fixture", "--source-manifest", "/input/source-manifest.json", "--max-bytes", "9", "--max-age-seconds", "8", "--min-free-bytes", "7", "--growth-bytes", "6", "--store-dir", "/cache/pnpm-store"}} {
		t.Run([]string{"plan", "explicit-false", "apply-bounds"}[index], func(t *testing.T) {
			t.Parallel()
			failure := errors.New("owned helper stopped")
			var got []string
			var out bytes.Buffer
			command := New(runtimeFor("/unused"), Dependencies{DependencyCache: func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
				got = args
				if stdout != &out || stderr == nil || ctx == nil {
					t.Fatal("helper lost its streams/context")
				}
				return failure
			}})
			err := execute(command, &out, append([]string{"dependency-cache"}, flags...)...)
			if !errors.Is(err, failure) {
				t.Fatalf("helper failure = %v", err)
			}
			var want []string
			if len(flags) > 1 {
				want = []string{"--apply", "--growth-bytes", "6", "--image", "sha256:fixture", "--max-age-seconds", "8", "--max-bytes", "9", "--min-free-bytes", "7", "--source-manifest", "/input/source-manifest.json", "--store-dir", "/cache/pnpm-store"}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("helper argv = %q, want %q", got, want)
			}
		})
	}
}

func TestDependencyCacheRefusesMissingOperationAndPositionalPaths(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"dependency-cache"}, {"dependency-cache", "/home/foreign-cache"}, {"dependency-cache", "--max-bytes", "invalid"}} {
		var out bytes.Buffer
		if err := execute(New(runtimeFor("/unused"), Dependencies{}), &out, args...); err == nil {
			t.Fatalf("accepted unsupported invocation %q", args)
		}
	}
}

type helperExitError struct{ code int }

func (e *helperExitError) Error() string { return "helper exited" }
func (e *helperExitError) ExitCode() int { return e.code }

func TestDependencyCachePreservesUsageAndFindingExitClassification(t *testing.T) {
	t.Parallel()
	for _, code := range []int{1, 2} {
		failure := &helperExitError{code: code}
		var out bytes.Buffer
		command := New(runtimeFor("/unused"), Dependencies{DependencyCache: func(context.Context, []string, io.Writer, io.Writer) error { return failure }})
		err := execute(command, &out, "dependency-cache")
		var usage *codedError
		if code == 2 && (!errors.As(err, &usage) || usage.code != 2) {
			t.Fatalf("usage identity lost: %v", err)
		}
		if code == 1 && !errors.Is(err, failure) {
			t.Fatalf("finding identity lost: %v", err)
		}
	}
}
