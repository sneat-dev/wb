package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

type createCommitOwnerFaultRunner struct {
	runner.Runner
	args     []string
	cause    error
	consumed int
}

func (r *createCommitOwnerFaultRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == "git" && reflect.DeepEqual(args, r.args) {
		r.consumed++
		return runner.Result{ExitCode: 1}, r.cause
	}
	return r.Runner.RunOpts(ctx, dir, options, name, args...)
}

func createCommitOwnerRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runEngineGit(t, dir, "init", "-b", "main")
	runEngineGit(t, dir, "config", "user.name", "WB Commit Owner Test")
	runEngineGit(t, dir, "config", "user.email", "commit-owner@example.test")
	writeEngineFile(t, filepath.Join(dir, "tracked.txt"), "original\n")
	writeEngineFile(t, filepath.Join(dir, ".gitignore"), ".worktree.md\n")
	runEngineGit(t, dir, "add", "-A")
	runEngineGit(t, dir, "commit", "-m", "initial")
	return dir
}

func TestCreateCommitOwnerNativeModesAndRefusals(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name, mode, wantCode                                        string
		land, secret, marker, empty, deletion, hook, rerun, invalid bool
	}{
		{name: "add isolated", mode: "add"}, {name: "invalid add", mode: "add", invalid: true, wantCode: CreateRefusalInvalidPath}, {name: "add tracked deletion", mode: "add", deletion: true},
		{name: "staged", mode: "staged"}, {name: "all", mode: "all"},
		{name: "add marker", mode: "add", marker: true, wantCode: CreateRefusalSecretPath},
		{name: "add directory secret", mode: "add", secret: true, wantCode: CreateRefusalSecretPath},
		{name: "all directory secret", mode: "all", secret: true, wantCode: CreateRefusalSecretPath},
		{name: "empty staged", mode: "staged", empty: true, wantCode: CreateRefusalNothingStaged},
		{name: "add landing leftover", mode: "add", land: true, wantCode: CreateRefusalLeftoverBeforeLanding},
		{name: "staged landing leftover", mode: "staged", land: true, wantCode: CreateRefusalLeftoverBeforeLanding},
		{name: "native hook refusal", mode: "all", hook: true},
		{name: "add rerun", mode: "add", rerun: true}, {name: "all rerun", mode: "all", rerun: true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			dir := createCommitOwnerRepository(t)
			before := runEngineGit(t, dir, "rev-parse", "HEAD")
			options := PullRequestCreateOptions{Message: "change", Land: row.land}
			path := "change.txt"
			if row.secret {
				path = "config/SeCrEt.PEM"
			}
			if row.marker {
				path = ".worktree.md"
			}
			if row.deletion {
				path = "tracked.txt"
				if err := os.Remove(filepath.Join(dir, path)); err != nil {
					t.Fatal(err)
				}
			} else if !row.empty && !row.rerun {
				writeEngineFile(t, filepath.Join(dir, path), "new\n")
			}
			if row.invalid {
				path = "../outside"
			}
			if row.rerun {
				path = "tracked.txt"
			}
			switch row.mode {
			case "add":
				options.Add = []string{path}
				if row.secret {
					options.Add = []string{"config"}
				}
			case "staged":
				options.CommitStaged = true
				if !row.empty {
					runEngineGit(t, dir, "add", "--", path)
				}
			case "all":
				options.CommitAll = true
			}
			// Another staged file must not enter the named-path commit; landing must refuse it.
			if row.mode == "add" && !row.secret && !row.marker && !row.rerun {
				writeEngineFile(t, filepath.Join(dir, "other.txt"), "other\n")
				runEngineGit(t, dir, "add", "other.txt")
			}
			if row.land && row.mode == "staged" {
				writeEngineFile(t, filepath.Join(dir, "leftover.txt"), "leftover\n")
			}
			if row.hook {
				writeEngineFile(t, filepath.Join(dir, ".git", "hooks", "pre-commit"), "#!/bin/sh\necho native-hook-refusal >&2\nexit 1\n")
				if err := os.Chmod(filepath.Join(dir, ".git", "hooks", "pre-commit"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			refusal, committed, err := performPullRequestCreateCommit(t.Context(), dir, options)
			after := runEngineGit(t, dir, "rev-parse", "HEAD")
			if row.hook {
				if err == nil || !strings.Contains(err.Error(), "native-hook-refusal") || after != before {
					t.Fatalf("hook=%v HEAD=%s before=%s", err, after, before)
				}
				return
			}
			if row.wantCode != "" {
				if err != nil || refusal == nil || refusal.code != row.wantCode || after != before {
					t.Fatalf("refusal=%+v err=%v HEAD=%s", refusal, err, after)
				}
				if row.secret && strings.TrimSpace(runEngineGit(t, dir, "diff", "--cached", "--name-only")) != "" {
					t.Fatal("secret remained staged")
				}
				return
			}
			if err != nil || refusal != nil {
				t.Fatalf("refusal=%+v err=%v", refusal, err)
			}
			if row.rerun {
				if after != before || committed != nil {
					t.Fatalf("rerun HEAD=%s committed=%v", after, committed)
				}
				return
			}
			if after == before || !reflect.DeepEqual(committed, []string{path}) {
				t.Fatalf("HEAD=%s committed=%v want=%s", after, committed, path)
			}
			if row.mode == "add" && strings.TrimSpace(runEngineGit(t, dir, "diff", "--cached", "--name-only")) != "other.txt" {
				t.Fatal("named commit consumed another staged path")
			}
		})
	}
}

func TestCreateCommitOwnerExactCommandFailures(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name, mode      string
		args            []string
		land, committed bool
	}{
		{"add stage", "add", []string{"add", "--", "change.txt"}, false, false},
		{"add staged read", "add", []string{"diff", "--cached", "--name-only", "-z", "--", "change.txt"}, false, false},
		{"add commit", "add", []string{"commit", "-m", "change", "--", "change.txt"}, false, false},
		{"add committed read", "add", []string{"diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"}, false, true},
		{"add land status", "add", []string{"status", "--porcelain=v1", "-z"}, true, false},
		{"staged index read", "staged", []string{"diff", "--cached", "--name-only"}, false, false},
		{"staged land status", "staged", []string{"status", "--porcelain=v1", "-z"}, true, false},
		{"staged commit", "staged", []string{"commit", "-m", "change"}, false, false},
		{"staged committed read", "staged", []string{"diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"}, false, true},
		{"all stage", "all", []string{"add", "-A"}, false, false},
		{"all staged read", "all", []string{"diff", "--cached", "--name-only", "-z"}, false, false},
		{"all commit", "all", []string{"commit", "-m", "change"}, false, false},
		{"all committed read", "all", []string{"diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"}, false, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			dir := createCommitOwnerRepository(t)
			writeEngineFile(t, filepath.Join(dir, "change.txt"), "change\n")
			before := runEngineGit(t, dir, "rev-parse", "HEAD")
			cause := errors.New("selected native stage refused")
			run := &createCommitOwnerFaultRunner{Runner: defaultRunner, args: row.args, cause: cause}
			options := PullRequestCreateOptions{Message: "change", Land: row.land, run: run}
			switch row.mode {
			case "add":
				options.Add = []string{"change.txt"}
			case "all":
				options.CommitAll = true
			case "staged":
				options.CommitStaged = true
				runEngineGit(t, dir, "add", "change.txt")
			}
			refusal, committed, err := performPullRequestCreateCommit(t.Context(), dir, options)
			after := runEngineGit(t, dir, "rev-parse", "HEAD")
			if refusal != nil || committed != nil || !errors.Is(err, cause) || run.consumed != 1 || (after != before) != row.committed {
				t.Fatalf("refusal=%+v committed=%v err=%v consumed=%d HEAD changed=%v", refusal, committed, err, run.consumed, after != before)
			}
		})
	}
}

func TestCreateCommitOwnerPathAndSecretContracts(t *testing.T) {
	t.Parallel()
	dir := createCommitOwnerRepository(t)
	writeEngineFile(t, filepath.Join(dir, "safe.txt"), "safe")
	for _, row := range []struct {
		name   string
		raw    []string
		want   []string
		reason string
	}{
		{"blank ignored", []string{" ", "safe.txt"}, []string{"safe.txt"}, ""},
		{"absolute existing", []string{filepath.Join(dir, "safe.txt")}, []string{"safe.txt"}, ""},
		{"root forbidden", []string{"."}, nil, "inside"}, {"escape", []string{"../outside"}, nil, "inside"},
		{"missing", []string{"absent"}, nil, "match nothing"},
		{"invalid before missing", []string{"absent", "../outside"}, nil, "inside"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveAddPaths(t.Context(), defaultRunner, dir, row.raw)
			if row.reason != "" {
				if err == nil || !strings.Contains(err.Error(), row.reason) {
					t.Fatalf("paths=%v err=%v", got, err)
				}
			} else if err != nil || !reflect.DeepEqual(got, row.want) {
				t.Fatalf("paths=%v err=%v", got, err)
			}
		})
	}
	if _, _, err := performPullRequestCreateCommit(t.Context(), dir, PullRequestCreateOptions{}); err == nil || !strings.Contains(err.Error(), "require -m") {
		t.Fatalf("message=%v", err)
	}
	if !reflect.DeepEqual(pullRequestCreateSecretPaths([]string{"safe", "x/.ENV.local", "x/.env", "x/cert.KEY", "cert.p12", "id_ed25519.pub", "id_ecdsa", "id_rsa.pub"}), []string{"x/.ENV.local", "x/.env", "x/cert.KEY", "cert.p12", "id_ed25519.pub", "id_ecdsa", "id_rsa.pub"}) {
		t.Fatal("secret classifications changed")
	}
	if isNothingToCommit(nil) || isNothingToCommit(errors.New("hook refused")) || !isNothingToCommit(errors.New("NO CHANGES ADDED TO COMMIT")) {
		t.Fatal("rerun error contract changed")
	}
	if !addedPathCovers([]string{"dir/"}, "dir/file") || addedPathCovers([]string{"dir"}, "directory/file") {
		t.Fatal("directory boundary changed")
	}
}

// TestCreateCommitOwnerControlledPathResolutionRefusesBeforeInspection is a
// controlled resolver-error contract. Real native Abs success is checked below;
// no removed-cwd or child-process outcome is claimed here.
func TestCreateCommitOwnerControlledPathResolutionRefusesBeforeInspection(t *testing.T) {
	t.Parallel()
	cause := errors.New("selected absolute-path resolution refusal")
	probe := runnertest.New(t)
	calls := 0
	got, err := resolveAddPathsWithPathResolver(t.Context(), probe, "relative", []string{"safe.txt"}, func(path string) (string, error) {
		calls++
		if path != "relative" {
			t.Fatalf("path resolver input=%q, want relative", path)
		}
		return "", cause
	})
	if got != nil || !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), "resolve worktree relative: ") || calls != 1 || len(probe.Calls()) != 0 {
		t.Fatalf("controlled resolver refusal paths=%v err=%v resolverCalls=%d runnerCalls=%v", got, err, calls, probe.Calls())
	}

	// The successful projection uses the actual native resolver and private file.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "safe.txt"), []byte("private input\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = resolveAddPathsWithPathResolver(t.Context(), probe, dir, []string{"safe.txt"}, filepath.Abs)
	if err != nil || !reflect.DeepEqual(got, []string{"safe.txt"}) || len(probe.Calls()) != 0 {
		t.Fatalf("native resolver success paths=%v err=%v runnerCalls=%v", got, err, probe.Calls())
	}
}
