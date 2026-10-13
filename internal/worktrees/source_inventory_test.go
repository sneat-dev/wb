package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestSourceCanonicalResolutionPreservesPhysicalCaseAndExactIdentity(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"legacy", "hosted", "explicit host", "absent", "ambiguous", "unrelated broken owner", "unreadable matching owner", "unreadable host", "unreadable root", "invalid repository", "missing root"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repository := "ACME/APP"
			expected := ""
			legacy := filepath.Join(root, "Acme", "App")
			hosted := filepath.Join(root, "github.com", "Acme", "App")
			mkdir := func(path string) {
				t.Helper()
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "legacy", "unrelated broken owner":
				mkdir(legacy)
				expected = legacy
			case "hosted", "explicit host":
				mkdir(hosted)
				expected = hosted
			case "ambiguous":
				mkdir(legacy)
				mkdir(hosted)
			case "unreadable matching owner":
				if err := os.WriteFile(filepath.Join(root, "Acme"), []byte("file"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unreadable host":
				if err := os.Symlink(filepath.Join(root, "github.com"), filepath.Join(root, "github.com")); err != nil {
					t.Fatal(err)
				}
			case "unreadable root":
				root = filepath.Join(root, "loop")
				if err := os.Symlink(root, root); err != nil {
					t.Fatal(err)
				}
			case "invalid repository":
				repository = "../app"
			case "missing root":
				root = filepath.Join(root, "missing")
			}
			if scenario == "explicit host" {
				repository = "github.com/ACME/APP"
			}
			// A regular file resembling a forge hostname cannot hide a repository.
			if scenario == "unrelated broken owner" {
				if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("notes"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "other"), filepath.Join(root, "other")); err != nil {
					t.Fatal(err)
				}
			}
			got, err := sourceCanonicalPath(root, repository)
			wantErr := strings.HasPrefix(scenario, "unreadable") || scenario == "ambiguous" || scenario == "invalid repository"
			if (err != nil) != wantErr || err == nil && got != expected {
				t.Fatalf("path=%q err=%v want=%q error=%t", got, err, expected, wantErr)
			}
		})
	}
}

func TestSourceRegistryRequiresCompleteUnambiguousGitEvidence(t *testing.T) {
	t.Parallel()
	head := strings.Repeat("a", 40)
	base := "worktree /canonical\x00HEAD " + head + "\x00branch refs/heads/main\x00\x00"
	for _, scenario := range []string{"valid", "detached", "unknown producer", "locked prunable", "empty", "truncated", "relative path", "unknown path", "duplicate path", "missing head", "malformed head", "duplicate head", "duplicate branch", "empty branch", "foreign ref", "branch and detached", "duplicate detached", "unknown field"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			raw := base
			switch scenario {
			case "detached":
				raw = strings.ReplaceAll(raw, "branch refs/heads/main", "detached")
			case "unknown producer":
				raw = strings.ReplaceAll(raw, "branch refs/heads/main\x00", "")
			case "locked prunable":
				raw = strings.ReplaceAll(raw, "branch refs/heads/main", "branch refs/heads/main\x00locked held\x00prunable missing")
			case "empty":
				raw = ""
			case "truncated":
				raw = strings.TrimSuffix(raw, "\x00")
			case "relative path":
				raw = strings.ReplaceAll(raw, "/canonical", "canonical")
			case "unknown path":
				raw = strings.ReplaceAll(raw, "worktree ", "unknown ")
			case "duplicate path":
				raw += raw
			case "missing head":
				raw = strings.ReplaceAll(raw, "HEAD "+head+"\x00", "")
			case "malformed head":
				raw = strings.ReplaceAll(raw, head, strings.Repeat("x", 40))
			case "duplicate head":
				raw = strings.ReplaceAll(raw, "HEAD "+head, "HEAD "+head+"\x00HEAD "+head)
			case "duplicate branch":
				raw = strings.ReplaceAll(raw, "branch refs/heads/main", "branch refs/heads/main\x00branch refs/heads/main")
			case "empty branch":
				raw = strings.ReplaceAll(raw, "refs/heads/main", "refs/heads/")
			case "foreign ref":
				raw = strings.ReplaceAll(raw, "refs/heads/main", "refs/remotes/main")
			case "branch and detached":
				raw = strings.ReplaceAll(raw, "branch refs/heads/main", "branch refs/heads/main\x00detached")
			case "duplicate detached":
				raw = strings.ReplaceAll(raw, "branch refs/heads/main", "detached\x00detached")
			case "unknown field":
				raw = strings.ReplaceAll(raw, "branch refs/heads/main", "branch refs/heads/main\x00mystery")
			}
			entries, err := parseSourceRegistry(raw)
			good := scenario == "valid" || scenario == "detached" || scenario == "unknown producer" || scenario == "locked prunable"
			if (err == nil) != good || good && (len(entries) != 1 || entries[0].head != head) {
				t.Fatalf("entries=%+v err=%v", entries, err)
			}
		})
	}
}

func TestSourceDetachedExclusionRequiresStablePhysicalGitIdentityAndCleanStatus(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"clean other head", "same head", "wrong registry head", "missing directory", "file directory", "bad identity", "identity error", "wrong root", "wrong common", "attached", "unknown registry state", "dirty tracked", "dirty untracked", "status error", "changed head", "changed tracked status", "changed untracked status", "final identity error", "final status error", "replaced root", "replaced common"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			path, common, other := t.TempDir(), t.TempDir(), t.TempDir()
			commonIdentity, err := os.Stat(common)
			if err != nil {
				t.Fatal(err)
			}
			localHead, sourceHead := strings.Repeat("b", 40), strings.Repeat("a", 40)
			entry := sourceRegistration{path: path, head: localHead, detached: true}
			before := path + "\n" + common + "\n" + localHead + "\nHEAD\n"
			after := before
			status, finalStatus := "", ""
			var identityErr, statusErr, finalErr, finalStatusErr error
			switch scenario {
			case "same head":
				sourceHead = localHead
			case "wrong registry head":
				entry.head = sourceHead
			case "missing directory":
				entry.path = filepath.Join(path, "missing")
			case "file directory":
				entry.path = filepath.Join(path, "file")
				if err := os.WriteFile(entry.path, []byte("file"), 0600); err != nil {
					t.Fatal(err)
				}
			case "bad identity":
				before = "partial"
			case "identity error":
				identityErr = errors.New("read failed")
			case "wrong root":
				before = strings.ReplaceAll(before, path, other)
			case "wrong common":
				before = strings.ReplaceAll(before, common, other)
			case "attached":
				before = strings.TrimSuffix(before, "HEAD\n") + "source\n"
			case "unknown registry state":
				entry.detached = false
			case "dirty tracked":
				status = " M source.go\n"
			case "dirty untracked":
				status = "?? source.go\n"
			case "status error":
				statusErr = errors.New("status failed")
			case "changed head":
				after = strings.ReplaceAll(after, localHead, sourceHead)
			case "changed tracked status":
				finalStatus = " M source.go\n"
			case "changed untracked status":
				finalStatus = "?? source.go\n"
			case "final identity error":
				finalErr = errors.New("final identity failed")
			case "final status error":
				finalStatusErr = errors.New("final status failed")
			}
			fake := runnertest.New(t)
			args := []string{"git", "--no-optional-locks", "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "HEAD", "--abbrev-ref", "HEAD"}
			statusArgs := []string{"git", "--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=all"}
			fake.ExpectArgv(args, runner.Result{Stdout: before}, identityErr)
			fake.ExpectArgv(statusArgs, runner.Result{Stdout: status}, statusErr)
			fake.Expect(func(call runnertest.Call) bool {
				if strings.Join(call.Argv(), "\x00") != strings.Join(args, "\x00") {
					return false
				}
				if scenario == "replaced root" || scenario == "replaced common" {
					target := path
					if scenario == "replaced common" {
						target = common
					}
					if err := os.Rename(target, target+"-retained"); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(target + "-retained") })
					if err := os.Mkdir(target, 0700); err != nil {
						t.Fatal(err)
					}
				}
				return true
			}, runner.Result{Stdout: after}, finalErr)
			fake.ExpectArgv(statusArgs, runner.Result{Stdout: finalStatus}, finalStatusErr)
			got, _, err := inspectSourceRegistration(t.Context(), SourceListOptions{HeadSHA: sourceHead, Runner: fake}, "", "", "", commonIdentity, entry)
			if (err == nil) != (scenario == "clean other head") || got != nil {
				t.Fatalf("result=%+v err=%v", got, err)
			}
		})
	}
}

func TestSourceInventoryScopesDiagnosticsAndFailsClosedOnRegistryOrIdentityErrors(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"empty source selection", "missing projects root", "duplicate physical producer", "missing canonical", "invalid repository", "unknown head", "invalid branch", "missing canonical git", "canonical identity error", "canonical identity truncated", "canonical root mismatch", "canonical common mismatch", "registry error", "registry malformed", "invalid registered branch", "canonical registration mismatch", "source directory missing", "source claim missing", "unknown producer", "clean detached other head", "detached source head", "registry changed", "final registry error", "canonical root replaced", "canonical common replaced"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			canonical := filepath.Join(root, "acme", "app")
			common := filepath.Join(canonical, ".git")
			path := filepath.Join(root, "source")
			other := t.TempDir()
			for _, dir := range []string{common, path} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			head := strings.Repeat("a", 40)
			localHead := strings.Repeat("b", 40)
			options := SourceListOptions{ProjectsRoot: root, Repository: "Acme/App", Branch: "source", HeadSHA: head}
			canonicalIdentity := canonical + "\n" + common + "\n" + common + "\n"
			registry := "worktree " + canonical + "\x00HEAD " + localHead + "\x00branch refs/heads/main\x00\x00"
			var canonicalErr, registryErr, afterErr error
			switch scenario {
			case "missing projects root":
				options.ProjectsRoot = ""
			case "duplicate physical producer":
				alias := filepath.Join(root, "source-alias")
				if err := os.Symlink(path, alias); err != nil {
					t.Fatal(err)
				}
				registry += "worktree " + path + "\x00HEAD " + localHead + "\x00branch refs/heads/source\x00\x00"
				registry += "worktree " + alias + "\x00HEAD " + localHead + "\x00branch refs/heads/source\x00\x00"
			case "missing canonical":
				options.Repository = "absent/repo"
			case "invalid repository":
				options.Repository = "../repo"
			case "unknown head":
				options.HeadSHA = ""
			case "invalid branch":
				options.Branch = ""
			case "missing canonical git":
				if err := os.Remove(common); err != nil {
					t.Fatal(err)
				}
			case "canonical identity error":
				canonicalErr = errors.New("canonical read failed")
			case "canonical identity truncated":
				canonicalIdentity = "partial"
			case "canonical root mismatch":
				canonicalIdentity = strings.ReplaceAll(canonicalIdentity, canonical+"\n", other+"\n")
			case "canonical common mismatch":
				canonicalIdentity = strings.ReplaceAll(canonicalIdentity, common, other)
			case "registry error":
				registryErr = errors.New("registry read failed")
			case "registry malformed":
				registry = "partial"
			case "invalid registered branch":
				registry = strings.ReplaceAll(registry, "refs/heads/main", "refs/heads/bad..branch")
			case "canonical registration mismatch":
				registry = strings.ReplaceAll(registry, canonical, other)
			case "source directory missing", "source claim missing":
				registry += "worktree " + path + "\x00HEAD " + localHead + "\x00branch refs/heads/source\x00\x00"
			case "unknown producer":
				registry += "worktree " + path + "\x00HEAD " + localHead + "\x00\x00"
			case "clean detached other head":
				registry += "worktree " + path + "\x00HEAD " + localHead + "\x00detached\x00\x00"
			case "detached source head":
				localHead = head
				registry += "worktree " + path + "\x00HEAD " + localHead + "\x00detached\x00\x00"
			case "final registry error":
				afterErr = errors.New("final registry failed")
			}
			if scenario == "source directory missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			fake := runnertest.New(t)
			options.Runner = fake
			fake.ExpectArgv([]string{"git", "--no-optional-locks", "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir"}, runner.Result{Stdout: canonicalIdentity}, canonicalErr)
			registryArgs := []string{"git", "--no-optional-locks", "worktree", "list", "--porcelain", "-z"}
			fake.ExpectArgv(registryArgs, runner.Result{Stdout: registry}, registryErr)
			identityArgs := []string{"git", "--no-optional-locks", "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "HEAD", "--abbrev-ref", "HEAD"}
			identity := path + "\n" + common + "\n" + localHead + "\nHEAD\n"
			if scenario == "source claim missing" || scenario == "duplicate physical producer" {
				identity = strings.TrimSuffix(identity, "HEAD\n") + "source\n"
			}
			fake.ExpectArgv(identityArgs, runner.Result{Stdout: identity}, nil)
			statusArgs := []string{"git", "--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=all"}
			fake.ExpectArgv(statusArgs, runner.Result{}, nil)
			if scenario == "clean detached other head" {
				fake.ExpectArgv(identityArgs, runner.Result{Stdout: identity}, nil)
				fake.ExpectArgv(statusArgs, runner.Result{}, nil)
			}
			after := registry
			if scenario == "registry changed" {
				after = strings.ReplaceAll(after, localHead, head)
			}
			fake.Expect(func(call runnertest.Call) bool {
				if strings.Join(call.Argv(), "\x00") != strings.Join(registryArgs, "\x00") {
					return false
				}
				if scenario == "canonical root replaced" || scenario == "canonical common replaced" {
					target := canonical
					if scenario == "canonical common replaced" {
						target = common
					}
					if err := os.Rename(target, target+"-retained"); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(target + "-retained") })
					if err := os.MkdirAll(common, 0700); err != nil {
						t.Fatal(err)
					}
				}
				return true
			}, runner.Result{Stdout: after}, afterErr)
			result, err := ListSourceWithDiagnostics(t.Context(), options)
			diagnostic := scenario == "duplicate physical producer" || scenario == "source directory missing" || scenario == "source claim missing" || scenario == "unknown producer" || scenario == "detached source head"
			success := scenario == "empty source selection" || scenario == "missing canonical" || scenario == "clean detached other head"
			wantDiagnostics := 1
			if scenario == "duplicate physical producer" {
				wantDiagnostics = 2
			}
			if (err == nil) != (success || diagnostic) || diagnostic && len(result.Diagnostics) != wantDiagnostics || success && (len(result.Diagnostics) != 0 || len(result.Results) != 0) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
