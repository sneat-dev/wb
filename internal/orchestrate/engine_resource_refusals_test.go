package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestEngineNormalizationResourceFailurePreservesOrder(t *testing.T) {
	t.Parallel()
	cause := errors.New("absolute path unavailable")
	input := Options{GitHubDir: "relative-root", Operation: "operation", Merge: true, Verify: true}
	calls := 0
	got, err := normalizeWithAbs(input, func(path string) (string, error) {
		calls++
		if path != input.GitHubDir {
			t.Fatalf("absolute input %q", path)
		}
		return "", cause
	})
	if !errors.Is(err, cause) || !reflect.DeepEqual(got, Options{}) || calls != 1 || input.Branch != "" || input.Commit || input.Push || input.PR || len(input.Checks) != 0 {
		t.Fatalf("failed normalization %+v %v calls=%d input=%+v", got, err, calls, input)
	}
	got, err = normalizeWithAbs(Options{GitHubDir: " ", Operation: "operation"}, func(string) (string, error) { t.Fatal("empty-root validation must precede Abs"); return "", cause })
	if err == nil || err.Error() != "GitHub directory is required" || !reflect.DeepEqual(got, Options{}) {
		t.Fatalf("validation order %+v %v", got, err)
	}
}

type engineNonregularGitInfo struct{ os.FileInfo }

func (engineNonregularGitInfo) Mode() os.FileMode { return os.ModeSocket }
func (engineNonregularGitInfo) IsDir() bool       { return false }

func TestEngineFilesystemInspectionRefusalsPreserveCustody(t *testing.T) {
	t.Parallel()
	if guard, err := managedInputWorktreeWithLstat(context.Background(), "", "acme/app", Options{}, func(string) (os.FileInfo, error) {
		t.Fatal("unsupplied input must not inspect filesystem")
		return nil, errors.New("unexpected inspection")
	}); guard != nil || err != nil {
		t.Fatalf("unsupplied input should permit canonical allocation: %+v %v", guard, err)
	}
	t.Run("root_lstat", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		path := filepath.Join(root, strings.Repeat("x", 1024))
		_, nativeErr := os.Lstat(path)
		var expected *os.PathError
		if !errors.As(nativeErr, &expected) || errors.Is(nativeErr, os.ErrNotExist) {
			t.Fatalf("fixture must produce native nonmissing error: %v", nativeErr)
		}
		guard, err := managedInputWorktree(context.Background(), path, "acme/app", Options{GitHubDir: root, Ref: "main"})
		var actual *os.PathError
		if guard != nil || !errors.As(err, &actual) || actual.Op != expected.Op || actual.Path != expected.Path || !errors.Is(err, expected.Err) || !strings.Contains(err.Error(), "inspect supplied repository path") {
			t.Fatalf("inspection %+v %v", guard, err)
		}
		entries, listErr := os.ReadDir(root)
		if listErr != nil || len(entries) != 0 {
			t.Fatalf("inspection wrote state %v %v", entries, listErr)
		}
	})
	for _, kind := range []string{"git_lstat", "nonregular_git"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			cause := errors.New("Git entry unavailable")
			var probes []string
			rootInfo, err := os.Lstat(root)
			if err != nil {
				t.Fatal(err)
			}
			guard, err := managedInputWorktreeWithLstat(context.Background(), root, "acme/app", Options{GitHubDir: root, Ref: "main"}, func(path string) (os.FileInfo, error) {
				probes = append(probes, path)
				if path == root {
					return os.Lstat(path)
				}
				if path != filepath.Join(root, ".git") {
					t.Fatalf("unexpected probe %q", path)
				}
				if kind == "git_lstat" {
					return nil, &os.PathError{Op: "lstat", Path: path, Err: cause}
				}
				return engineNonregularGitInfo{rootInfo}, nil
			})
			if guard != nil || err == nil || !reflect.DeepEqual(probes, []string{root, filepath.Join(root, ".git")}) {
				t.Fatalf("Git inspection %+v %v probes=%v", guard, err, probes)
			}
			if kind == "git_lstat" && (!errors.Is(err, cause) || !strings.Contains(err.Error(), "inspect supplied Git entry")) {
				t.Fatalf("Git probe cause %v", err)
			}
			if kind == "nonregular_git" && !strings.Contains(err.Error(), "directory or regular gitdir file") {
				t.Fatalf("Git type refusal %v", err)
			}
			entries, listErr := os.ReadDir(root)
			if listErr != nil || len(entries) != 0 {
				t.Fatalf("classification wrote authority state %v %v", entries, listErr)
			}
		})
	}
	t.Run("canonical_mkdir", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		canonical := filepath.Join(root, "owner", "repository")
		cause := errors.New("parent allocation refused")
		calls := 0
		fake := runnertest.New(t)
		got, err := ensureCanonicalWithMkdirAll(context.Background(), Repository{Slug: "acme/app"}, canonical, Options{run: fake}, func(path string, mode os.FileMode) error {
			calls++
			if path != filepath.Dir(canonical) || mode != 0755 {
				t.Fatalf("allocation %q %v", path, mode)
			}
			return cause
		})
		if got != (ResolvedBase{}) || err != cause || calls != 1 {
			t.Fatalf("allocation result %+v %v calls=%d", got, err, calls)
		}
		entries, listErr := os.ReadDir(root)
		if listErr != nil || len(entries) != 0 {
			t.Fatalf("allocation mutated root %v %v", entries, listErr)
		}
	})
	t.Run("canonical_stat", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "invalid\x00repository")
		_, nativeErr := os.Stat(path)
		var expected *os.PathError
		if !errors.As(nativeErr, &expected) || errors.Is(nativeErr, os.ErrNotExist) {
			t.Fatalf("fixture stat error %v", nativeErr)
		}
		got, err := ensureCanonicalWithMkdirAll(context.Background(), Repository{Slug: "acme/app"}, path, Options{run: runnertest.New(t)}, func(string, os.FileMode) error { t.Fatal("stat error must precede allocation"); return nil })
		var actual *os.PathError
		if got != (ResolvedBase{}) || !errors.As(err, &actual) || actual.Path != expected.Path || actual.Op != expected.Op || !errors.Is(err, expected.Err) {
			t.Fatalf("stat result %+v %v", got, err)
		}
	})
}

type engineReadFailureReporter struct {
	Handler[string]
	t *testing.T
}

func (r engineReadFailureReporter) AppliedFiles(string) []string {
	r.t.Fatal("status failure must preempt reporter")
	return nil
}

func TestEngineChangedFileReadFailurePreemptsReporter(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cause := errors.New("status unavailable")
	before := map[string]string{"kept.txt": "??"}
	fake := runnertest.New(t)
	fake.Expect(func(call runnertest.Call) bool {
		return call.Op == "RunOpts" && call.Dir == root && reflect.DeepEqual(call.Argv(), []string{"git", "status", "--porcelain=v1", "-z"}) && reflect.DeepEqual(call.Opts.Env, console.Env())
	}, runner.Result{Stderr: "native status diagnostic"}, cause)
	got, err := changedFilesSince(context.Background(), root, before, engineReadFailureReporter{t: t}, "unchanged metadata", Options{run: fake})
	if got != nil || !errors.Is(err, cause) || !strings.Contains(err.Error(), "native status diagnostic") || !reflect.DeepEqual(before, map[string]string{"kept.txt": "??"}) {
		t.Fatalf("status failure %v %v before=%v", got, err, before)
	}
}

func TestEngineChangedFilesSinceExcludesUnchangedStatus(t *testing.T) {
	t.Parallel()
	before := map[string]string{"unchanged.txt": "??", "changed.txt": " M"}
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "status", "--porcelain=v1", "-z"}, runner.Result{Stdout: "?? unchanged.txt\x00?? changed.txt\x00?? added.txt\x00"}, nil)
	got, err := changedFilesSince(context.Background(), t.TempDir(), before, textHandler{}, "metadata", Options{run: fake})
	if err != nil || !reflect.DeepEqual(got, []string{"added.txt", "changed.txt"}) || !reflect.DeepEqual(before, map[string]string{"unchanged.txt": "??", "changed.txt": " M"}) {
		t.Fatalf("status delta %v %v before=%v", got, err, before)
	}
}

func TestEngineNormalizePushImpliesCommitWithoutPullRequest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	input := Options{GitHubDir: root, Operation: "push-contract", Push: true, Model: "retained-model", Prompt: "retained prompt", Retry: 2}
	got, err := Normalize(input)
	if err != nil || !got.Push || !got.Commit || got.PR || got.Merge || got.GitHubDir != root || got.Operation != input.Operation || got.Model != input.Model || got.Prompt != input.Prompt || got.Retry != input.Retry || got.Branch != "wb/push-contract" || got.Ref != "main" || got.Parallel != 1 {
		t.Fatalf("push normalization %+v %v", got, err)
	}
	if input.Commit || input.PR || input.Merge || input.Branch != "" || input.Ref != "" || input.Parallel != 0 {
		t.Fatalf("caller options mutated %+v", input)
	}
}
