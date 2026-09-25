package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type retireStatusProvider struct {
	*slowStatusProvider
	status remotestate.StatusSnapshot
	err    error
}

func (provider *retireStatusProvider) Status(context.Context) (remotestate.StatusSnapshot, error) {
	return provider.status, provider.err
}

// TestWorktreeRetireDryRunOnEmptyProjectsRootReportsNoMatch drives "wb
// worktree retire" through the real CLI dispatch (not just a structural flag
// check), so the RunE closure that reads inv.projectsRoot into
// worktrees.RetireOptions.ProjectsRoot actually executes and reaches
// worktrees.Retire's own repository-count refusal. An empty projects root
// has no managed repository for any task, with or without --filter, so this
// does not exercise inv.filterFlag specifically; it asserts the exact
// refusal text rather than any non-zero exit, so a caller who breaks that
// message (or starts matching a phantom repository) fails here.
func TestWorktreeRetireDryRunOnEmptyProjectsRootReportsNoMatch(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"worktree", "retire", "no-such-task", "--projects-root", root}
	if code := run(args, &stdout, &stderr); code == 0 {
		t.Fatalf("run(%q) exit = 0, want a failure for a task that has no worktree, stdout=%s", args, stdout.String())
	}
	const want = "error: retirement requires exactly one managed repository; found 0\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

// fakeGHForRetiredArchivePreflight answers exactly the one `gh api
// repos/<org>/backstage-retired` call PlanRetiredArchivePreflight makes, so
// a real "wb worktree retire" dry run can get past the archive-repository
// check and reach the remote-ownership closure beyond it.
func fakeGHForRetiredArchivePreflight(t *testing.T, archiveRepository string) {
	t.Helper()
	binDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nprintf '{\"full_name\":\"%s\",\"private\":true}\\n'\n", archiveRepository)
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestWorktreeRetireDryRunReachesTheRemoteOwnershipCheck proves the RunE
// closure that wires retireCheckRemoteOwnership into worktrees.Retire
// (worktree_retire.go:52) is actually reached from a real worktree, not just
// invoked directly the way TestRetireRemoteOwnershipChecksClaimsAndMachineSnapshots
// exercises the function itself. It fakes `gh` only enough to clear the
// archive-repository preflight that runs immediately before that closure;
// with no real remote task store configured, the closure itself still
// refuses, but only after the CLI wiring and worktrees.Retire's preflight
// chain have both actually run.
func TestWorktreeRetireDryRunReachesTheRemoteOwnershipCheck(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	fakeGHForRetiredArchivePreflight(t, "acme/backstage-retired")
	prompt := writeOriginalPromptFixture(t, "retire remote ownership fixture")

	var stdout, stderr bytes.Buffer
	createArgs := []string{"--projects-root", projects, "worktree", "create", "cli-retire-ownership", "acme/app", "--model", "unknown", "--original-prompt-file", prompt}
	if code := run(createArgs, &stdout, &stderr); code != exitOK {
		t.Fatalf("worktree create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	retireArgs := []string{"--projects-root", projects, "worktree", "retire", "cli-retire-ownership"}
	code := run(retireArgs, &stdout, &stderr)
	if code == exitOK {
		t.Fatalf("retire without a configured remote task store must be refused; stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "retirement remote owner preflight") {
		t.Fatalf("stderr = %q, want the remote-ownership preflight refusal", stderr.String())
	}
}

// redirectGitHubURLToLocalBareRepo rewrites one exact GitHub remote URL to a
// local bare repository for every git subprocess this test starts directly
// or transitively (via GIT_CONFIG_COUNT/KEY_N/VALUE_N, the same mechanism
// internal/testenv.SetGitAutoMaintenanceOff uses), so a production code path
// that is hard-coded to a real GitHub remote can be driven end to end
// against a throwaway local repository instead.
func redirectGitHubURLToLocalBareRepo(t *testing.T, githubURL, localPath string) {
	t.Helper()
	count := 0
	if parsed, err := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT")); err == nil && parsed > count {
		count = parsed
	}
	t.Setenv("GIT_CONFIG_KEY_"+strconv.Itoa(count), "url.file://"+localPath+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_"+strconv.Itoa(count), githubURL)
	t.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(count+1))
}

// TestWorktreeRetireApplyCompletesAndReleasesTheRemoteClaim proves `wb
// worktree retire --apply` reaches worktree_retire.go:60 (the
// retireReleaseClaim call, gated on apply && result.Phase == "complete"):
// every existing retire test either dry-runs or is refused early, so a full
// completion was never exercised. worktree_retire.go hard-codes both the
// archive push remote and the remote task store to real GitHub URLs with no
// override, so this redirects those two exact URLs to local bare
// repositories via git's own url.insteadOf configuration (the standard
// mechanism for rewriting a remote transport) rather than touching
// production code, and fakes `gh` only enough to answer the two read-only
// GitHub API calls (the archive repository's existence/visibility, and the
// authenticated login) that remain on the path.
func TestWorktreeRetireApplyCompletesAndReleasesTheRemoteClaim(t *testing.T) {
	setGitIdentity(t)
	projects := setUpRenameCLIFixture(t)

	archive := filepath.Join(t.TempDir(), "archive.git")
	runCLIWorktreeGit(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", archive)
	testenv.ConfigureGitAutoMaintenanceOff(t, archive)
	redirectGitHubURLToLocalBareRepo(t, "git@github.com:acme/backstage-retired.git", archive)

	stateStore := filepath.Join(t.TempDir(), "wb-state.git")
	runCLIWorktreeGit(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", stateStore)
	testenv.ConfigureGitAutoMaintenanceOff(t, stateStore)
	redirectGitHubURLToLocalBareRepo(t, "git@github.com:acme/wb-state.git", stateStore)

	binDir := t.TempDir()
	ghScript := "#!/bin/sh\n" +
		"if [ \"$1\" = api ] && [ \"$2\" = user ]; then printf '{\"login\":\"alice\"}\\n'; exit 0; fi\n" +
		"case \"$2\" in\n" +
		"  *backstage-retired*) printf '{\"full_name\":\"acme/backstage-retired\",\"private\":true}\\n'; exit 0 ;;\n" +
		"  *) printf '[]\\n'; exit 0 ;;\n" +
		"esac\n"
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte(ghScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	configHome := filepath.Join(t.TempDir(), "xdg")
	if err := os.MkdirAll(filepath.Join(configHome, "wb"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "wb", "wb.yaml"), []byte("remote:\n  provider: git\n  repo: acme/wb-state\n  machine: retire-test-machine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)

	prompt := writeOriginalPromptFixture(t, "retire apply completion fixture")
	var stdout, stderr bytes.Buffer
	createArgs := []string{"--projects-root", projects, "worktree", "create", "cli-retire-apply", "acme/app", "--model", "unknown", "--original-prompt-file", prompt}
	if code := run(createArgs, &stdout, &stderr); code != exitOK {
		t.Fatalf("worktree create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	retireArgs := []string{"--projects-root", projects, "worktree", "retire", "cli-retire-apply", "--apply", "--format", "json"}
	code := run(retireArgs, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("retire --apply exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var result struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("retire --apply JSON is not parseable: %v\n%s", err, stdout.String())
	}
	if result.Phase != "complete" {
		t.Fatalf("retire --apply phase = %q, want complete\nstdout=%s\nstderr=%s", result.Phase, stdout.String(), stderr.String())
	}
}

func TestRetireRemoteOwnershipChecksClaimsAndMachineSnapshots(t *testing.T) {
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("remote:\n  repo: team/wb-state\n  machine: laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &retireStatusProvider{slowStatusProvider: &slowStatusProvider{}}
	deps := remoteDeps{configPath: config, login: func() (string, error) { return "alice", nil },
		open: func(remotestate.Config, string) (remotestate.Provider, error) { return provider, nil }}
	for _, tc := range []struct {
		name   string
		status remotestate.StatusSnapshot
		err    error
		want   string
	}{
		{name: "own claim and checkout", status: remotestate.StatusSnapshot{
			Claims:   []remotestate.ClaimEntry{{Claim: remotestate.Claim{Task: "retire-task", Login: "alice", Machine: "laptop"}}},
			Machines: []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "alice", Machine: "laptop", Worktrees: []remotestate.WorktreeState{{Task: "retire-task"}}}}},
		}},
		{name: "remote claim", status: remotestate.StatusSnapshot{Claims: []remotestate.ClaimEntry{{Claim: remotestate.Claim{Task: "retire-task", Login: "alice", Machine: "vm"}}}}, want: "competing remote claim"},
		{name: "remote checkout", status: remotestate.StatusSnapshot{Machines: []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "alice", Machine: "vm", Worktrees: []remotestate.WorktreeState{{Task: "retire-task"}}}}}}, want: "competing remote checkout"},
		{name: "unreadable claim", status: remotestate.StatusSnapshot{Claims: []remotestate.ClaimEntry{{Claim: remotestate.Claim{Task: "other-task"}, Error: "corrupt"}}}, want: "unreadable"},
		{name: "unreadable snapshot", status: remotestate.StatusSnapshot{Machines: []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "bob", Machine: "vm"}, Error: "corrupt"}}}, want: "unreadable remote machine snapshot"},
		{name: "store unavailable", err: errors.New("offline"), want: "read remote task ownership"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider.status, provider.err = tc.status, tc.err
			err := retireCheckRemoteOwnership(context.Background(), deps, "/projects", "retire-task")
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("remote ownership: %v, want %q", err, tc.want)
			}
		})
	}
	deps.configPath = filepath.Join(t.TempDir(), "missing.yaml")
	if err := retireCheckRemoteOwnership(context.Background(), deps, "/projects", "retire-task"); err == nil {
		t.Fatal("missing remote config was accepted")
	}
}

func TestRetireReleaseClaimOnlyAfterLastTaskWorktree(t *testing.T) {
	for _, tc := range []struct {
		name       string
		inventory  worktrees.ListOutcome
		listErr    error
		release    autoReleaseResult
		wantCalled bool
		wantStatus string
	}{
		{name: "last checkout", wantCalled: true, wantStatus: "released"},
		{name: "another repository remains", inventory: worktrees.ListOutcome{Results: []worktrees.ListResult{{Task: "task", Repository: "acme/other"}}}, wantStatus: "skipped"},
		{name: "malformed candidate remains", inventory: worktrees.ListOutcome{Diagnostics: []worktrees.ListDiagnostic{{}}}, wantStatus: "failed"},
		{name: "inventory unavailable", listErr: errors.New("offline"), wantStatus: "failed"},
		{name: "release unavailable", release: autoReleaseResult{Outcome: "disabled"}, wantCalled: true, wantStatus: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			called := false
			result := retireReleaseClaim(context.Background(), "/projects", "task", &out,
				func(_ context.Context, options worktrees.ListOptions) (worktrees.ListOutcome, error) {
					if options.ProjectsRoot != "/projects" || options.Task != "task" || options.Filter != "" {
						t.Fatalf("unexpected task inventory options: %#v", options)
					}
					return tc.inventory, tc.listErr
				},
				func(root, task string, writer io.Writer) autoReleaseResult {
					called = true
					if root != "/projects" || task != "task" {
						t.Fatalf("release target: %s %s", root, task)
					}
					_, _ = io.WriteString(writer, "remote claim: released task\n")
					if tc.release.Outcome != "" {
						return tc.release
					}
					return autoReleaseResult{Outcome: "released"}
				})
			if called != tc.wantCalled || result.Outcome != tc.wantStatus {
				t.Fatalf("called=%t result=%#v output=%q", called, result, out.String())
			}
			if result.Leaked() != (tc.wantStatus == "failed") {
				t.Fatalf("release failure visibility: %#v", result)
			}
			if tc.wantCalled && !strings.Contains(out.String(), "remote claim: released task") {
				t.Fatalf("missing release receipt: %q", out.String())
			}
		})
	}
}
