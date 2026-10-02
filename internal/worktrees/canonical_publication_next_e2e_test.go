//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestE2ECanonicalPublicationRepositoryAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		input      []string
		diagnostic string
	}{
		{"empty", nil, "at least one owner/repository"},
		{"malformed", []string{"owner/repo/extra"}, ""},
		{"duplicate", []string{"owner/repo", "owner/repo"}, "was supplied more than once"},
		{"case identity", []string{"Owner/repo", "owner/Repo"}, "duplicates case-insensitive identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			before := append([]string(nil), tc.input...)
			got, err := ValidateRepositories(tc.input)
			if err == nil || got != nil || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("ValidateRepositories = %v, %v", got, err)
			}
			if !reflect.DeepEqual(before, tc.input) {
				t.Fatalf("input changed: %v -> %v", before, tc.input)
			}
		})
	}
	input := []string{"z/repo", "a/repo"}
	got, err := ValidateRepositories(input)
	if err != nil || !reflect.DeepEqual(got, []string{"a/repo", "z/repo"}) || input[0] != "z/repo" {
		t.Fatalf("sorted admission = %v, %v; input %v", got, err, input)
	}
}

func TestE2ECanonicalPublicationLocalNamespaceAdmission(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	canonical := filepath.Join(projects, "github.com", "owner", "repo")
	common := filepath.Join(canonical, ".git")
	local := filepath.Join(canonical, ".worktrees")
	for _, root := range []string{local, canonical, filepath.Join(local, "task", "nested"), filepath.Join(local, "bad task"), filepath.Join(projects, "elsewhere")} {
		if got, ok := locateCanonicalLocalWorktree(projects, root, common); ok || !reflect.DeepEqual(got, managedWorktreeLocation{}) {
			t.Fatalf("unsafe location %s = %+v, %v", root, got, ok)
		}
	}
	root := filepath.Join(local, "task")
	got, ok := locateCanonicalLocalWorktree(projects, root, common)
	if !ok || got.Task != "task" || got.Repository != "repo" || got.Worktree != root || !got.Layout.Local {
		t.Fatalf("local admission = %+v, %v", got, ok)
	}
	if got, err := locateAdoptedWorktree(context.Background(), projects, root, wbhome.Layout{}, "bad task"); err == nil || !reflect.DeepEqual(got, managedWorktreeLocation{}) || !strings.Contains(err.Error(), "invalid task identity") {
		t.Fatalf("adopted admission = %+v, %v", got, err)
	}
}

func TestE2ECanonicalPublicationPolicyAndHexAdmission(t *testing.T) {
	t.Parallel()
	if canonicalPolicyGitArgumentsAllowed(nil) {
		t.Fatal("empty policy query admitted")
	}
	if got, err := gitCanonicalPolicyBytes(context.Background(), nil); err == nil || got != nil || !strings.Contains(err.Error(), "unsupported canonical policy Git query") {
		t.Fatalf("empty policy query = %q, %v", got, err)
	}
	for _, tc := range []struct {
		value string
		want  bool
	}{{"", false}, {"09abcdef", true}, {"abcdefg", false}, {"ABC", false}, {"é", false}} {
		if got := isLowerHex(tc.value); got != tc.want {
			t.Fatalf("hex %q = %v", tc.value, got)
		}
	}
}

func TestE2ECanonicalPublicationLookupBoundaryPreservesAuthority(t *testing.T) {
	t.Parallel()
	canonical := reconciliationFakeCanonical(t)
	missing := filepath.Join(t.TempDir(), "missing-executable-source")
	_, cause := os.ReadFile(missing)
	if !errors.Is(cause, os.ErrNotExist) {
		t.Fatalf("native resolver fixture = %v", cause)
	}
	calls := 0
	resolve := func() (string, error) { calls++; return "", cause }
	ctx := context.Background()
	// This is an injected lookup boundary carrying an independently captured
	// native cause; it does not claim os.Executable failed on this host.
	for _, tc := range []struct {
		name, diagnostic string
		run              func() ([]byte, error)
	}{
		{"canonical", "locate WB canonical Git helper", func() ([]byte, error) {
			return runCanonicalGitBytesWithExecutable(ctx, canonical, SecureCanonicalGitHelperArgument, "canonical Git", func() []string { t.Fatal("environment evaluated after failed lookup"); return nil }, resolve, trustedGitExecutable, "status")
		}},
		{"stage", "locate WB secure staging helper", func() ([]byte, error) {
			return runSecureStageHelperWithExecutable(ctx, canonical.root, resolve, secureStagePathArgument)
		}},
		{"staged canonical", "locate WB secure staged canonical Git helper", func() ([]byte, error) {
			return runSecureStageCanonicalGitHelperWithExecutable(ctx, canonical.root, canonical, canonical.path, "unused-git", "branch", "revision", false, resolve)
		}},
	} {
		//nolint:paralleltest // Serial cases share the resolver call counter and verify each exact increment before the final ownership control.
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			got, err := tc.run()
			if got != nil || !errors.Is(err, cause) || !strings.Contains(err.Error(), tc.diagnostic) || calls != before+1 {
				t.Fatalf("lookup = %q, %v; calls %d -> %d", got, err, before, calls)
			}
		})
	}
	before := calls
	got, err := runCanonicalGitBytesWithExecutable(ctx, nil, SecureCanonicalGitHelperArgument, "canonical Git", func() []string { t.Fatal("environment evaluated without ownership"); return nil }, resolve, trustedGitExecutable, "status")
	if got != nil || err == nil || !strings.Contains(err.Error(), "descriptors are unavailable") || calls != before {
		t.Fatalf("ownership before lookup = %q, %v; calls %d -> %d", got, err, before, calls)
	}
	if err := canonical.validate(); err != nil {
		t.Fatalf("lookup failure changed descriptor authority: %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookup created output: %v", err)
	}
}

func TestE2ECanonicalPublicationPlatformAdmissionOrder(t *testing.T) {
	t.Parallel()
	_, cause := os.ReadFile(filepath.Join(t.TempDir(), "missing-admission-source"))
	if !errors.Is(cause, os.ErrNotExist) {
		t.Fatalf("native cause fixture = %v", cause)
	}
	// These are per-call boundary refusals, not a native Darwin capability
	// failure. The native default remains platform availability then trusted Git.
	var order []string
	available := func() error { order = append(order, "available"); return cause }
	executable := func() (string, error) { order = append(order, "executable"); return "", cause }
	if err := requireGitFilesystemCapabilityWithAdmission(available, executable); !errors.Is(err, cause) || !reflect.DeepEqual(order, []string{"available"}) {
		t.Fatalf("platform refusal = %v, order %v", err, order)
	}
	order = nil
	available = func() error { order = append(order, "available"); return nil }
	if err := requireGitFilesystemCapabilityWithAdmission(available, executable); !errors.Is(err, cause) || !reflect.DeepEqual(order, []string{"available", "executable"}) {
		t.Fatalf("executable refusal = %v, order %v", err, order)
	}
}

func TestE2ECanonicalPublicationTrustedLookupBoundaries(t *testing.T) {
	t.Parallel()
	canonical := reconciliationFakeCanonical(t)
	_, cause := os.ReadFile(filepath.Join(t.TempDir(), "native-trusted-query-cause"))
	if !errors.Is(cause, os.ErrNotExist) {
		t.Fatalf("native cause = %v", cause)
	}
	resolveGit := func() (string, error) { return "", cause }
	ctx := context.Background()
	got, err := runCanonicalGitBytesWithExecutable(ctx, canonical, SecureCanonicalGitHelperArgument, "canonical Git", func() []string { t.Fatal("environment evaluated after trusted lookup refusal"); return nil }, os.Executable, resolveGit, "status")
	if got != nil || !errors.Is(err, cause) {
		t.Fatalf("trusted canonical lookup = %q, %v", got, err)
	}
	err = gitWorktreeAddFromStageDirectoryWithExecutable(ctx, canonical, canonical.path, canonical.root, "feature/unused", "revision", false, resolveGit)
	if !errors.Is(err, cause) {
		t.Fatalf("trusted staged lookup = %v", err)
	}
	// Fixed per-call lookup refusal carrying a native cause, not evidence that
	// the memoized platform developer-Git lookup failed on this host.
	if err := canonical.validate(); err != nil {
		t.Fatalf("lookup changed authority: %v", err)
	}
}

func TestE2ECanonicalPublicationEmptyOriginHeadResponse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, response := range []string{"", "origin/", "origin/main", "origin/development"} {
		t.Run(fmt.Sprintf("response-%q", response), func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			fake.ExpectArgv([]string{"git", "-C", root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"}, runner.Result{CombinedOutput: response + "\n"}, nil)
			ctx := withGitRunner(context.Background(), fake)
			want := "main"
			if response == "origin/development" {
				want = "development"
			}
			// Defensive query-response contract, not native blank symbolic-ref output.
			if got := guardDefaultBase(ctx, root); got != want || fake.CallCount() != 1 {
				t.Fatalf("default base response %q = %q; calls %d", response, got, fake.CallCount())
			}
		})
	}
}

func TestE2ECanonicalPublicationAbsentAdmissionNamespaces(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	outside := t.TempDir()
	if got, ok := locateCanonicalLocalWorktree(projects, filepath.Join(outside, ".worktrees", "task"), filepath.Join(outside, ".git")); ok || !reflect.DeepEqual(got, managedWorktreeLocation{}) {
		t.Fatalf("outside canonical placement admitted: %+v %t", got, ok)
	}
	if got, err := locateAdoptedWorktree(context.Background(), projects, outside, wbhome.Layout{}, "valid-task"); err == nil || !reflect.DeepEqual(got, managedWorktreeLocation{}) || !strings.Contains(err.Error(), "derive adopted worktree identity") {
		t.Fatalf("native missing Git authority: %+v %v", got, err)
	}
	if got, err := absoluteProjectsRoot("  "); got != "" || err == nil || !strings.Contains(err.Error(), "projects root is required") {
		t.Fatalf("blank root=%q %v", got, err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("admission changed missing authority: %v %v", entries, err)
	}
}
