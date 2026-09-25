package worktrees

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// wtLifeCovRepo is a minimal real Git canonical checkout whose root and Git
// directory descriptors the helper children inherit.
type wtLifeCovRepo struct {
	path   string
	head   string
	root   *os.File
	common *os.File
}

func wtLifeCovNewRepo(t *testing.T) *wtLifeCovRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "canonical")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	wtLifeCovGit(t, path, "init", "--quiet", "--initial-branch=main")
	wtLifeCovGit(t, path, "config", "user.name", "WB LifeCov")
	wtLifeCovGit(t, path, "config", "user.email", "wtlifecov@example.test")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtLifeCovGit(t, path, "add", "README.md")
	wtLifeCovGit(t, path, "commit", "--quiet", "-m", "initial")
	head := strings.TrimSpace(wtLifeCovGit(t, path, "rev-parse", "HEAD"))
	canonical, err := openCanonicalRepository(path)
	if err != nil {
		t.Fatalf("open canonical repository: %v", err)
	}
	t.Cleanup(canonical.close)
	return &wtLifeCovRepo{path: path, head: head, root: canonical.root, common: canonical.common}
}

func wtLifeCovGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = testenv.GitAutoMaintenanceOffEnv(append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

// wtLifeCovStage creates an operation root with one stage directory below it,
// mirroring the placement a secure worktree add uses.
func wtLifeCovStage(t *testing.T) (operationRoot, stage string, descriptor *os.File) {
	t.Helper()
	operationRoot = t.TempDir()
	stage = filepath.Join(operationRoot, "stage")
	if err := os.Mkdir(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	return operationRoot, stage, wtLifeCovOpenDirectory(t, stage)
}

// wtLifeCovScript writes an executable /bin/sh script.
func wtLifeCovScript(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := testenv.WriteExecutableFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// wtLifeCovNonExecutable writes an existing but non-executable file, so the
// platform capability backend must fail at its exec rather than run anything.
func wtLifeCovNonExecutable(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "git-not-executable")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func wtLifeCovTrustedGit(t *testing.T) string {
	t.Helper()
	executable, err := trustedGitExecutable()
	if err != nil {
		t.Fatalf("resolve trusted Git: %v", err)
	}
	return executable
}

// ---------------------------------------------------------------------------
// verifySecureStageContainment
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// RunSecureStageGitHelper
// ---------------------------------------------------------------------------

func TestWtLifeCovSecureStageHelperReportsHeldDirectory(t *testing.T) {
	_, stage, descriptor := wtLifeCovStage(t)
	result := wtLifeCovRunSecureHelper(t, "stage", []*os.File{descriptor}, []string{secureStagePathArgument})
	if result.exitCode != 0 {
		t.Fatalf("stage --path exit = %d, stderr = %q", result.exitCode, result.stderr)
	}
	got := strings.TrimSpace(wtLifeCovSection(result.stdout))
	if filepath.Clean(got) != filepath.Clean(stage) {
		t.Fatalf("stage --path printed %q, want %q (stdout %q)", got, stage, result.stdout)
	}
}

func TestWtLifeCovSecureStageHelperRejectsMalformedRequests(t *testing.T) {
	operationRoot, _, descriptor := wtLifeCovStage(t)
	cases := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{name: "missing operation", args: []string{}, wantStderr: "missing operation"},
		{name: "path with extra argument", args: []string{secureStagePathArgument, "extra"}, wantStderr: "invalid path arguments"},
		{name: "check without root", args: []string{secureStageCheckArgument}, wantStderr: "invalid containment check arguments"},
		{name: "git handoff without command", args: []string{operationRoot}, wantStderr: "invalid Git handoff arguments"},
		{name: "git handoff with relative executable", args: []string{operationRoot, "git", "status"}, wantStderr: "invalid Git handoff arguments"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result := wtLifeCovRunSecureHelper(t, "stage", []*os.File{descriptor}, testCase.args)
			if result.exitCode != 1 {
				t.Fatalf("exit = %d, want 1 (stderr %q)", result.exitCode, result.stderr)
			}
			if !strings.Contains(result.stderr, testCase.wantStderr) {
				t.Fatalf("stderr = %q, want substring %q", result.stderr, testCase.wantStderr)
			}
		})
	}
}

func TestWtLifeCovSecureStageHelperRejectsMissingStageDescriptor(t *testing.T) {
	t.Parallel()
	result := wtLifeCovRunSecureHelper(t, "stage", nil, []string{secureStagePathArgument})
	if result.exitCode != 1 {
		t.Fatalf("stage without descriptor exit = %d, stderr = %q", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stderr, "enter inherited stage directory") {
		t.Fatalf("stage without descriptor stderr = %q", result.stderr)
	}
}

func TestWtLifeCovSecureStageHelperChecksContainment(t *testing.T) {
	operationRoot, stage, descriptor := wtLifeCovStage(t)

	inside := wtLifeCovRunSecureHelper(t, "stage", []*os.File{descriptor}, []string{secureStageCheckArgument, operationRoot})
	if inside.exitCode != 0 {
		t.Fatalf("containment check inside root exit = %d, stderr = %q", inside.exitCode, inside.stderr)
	}

	equal := wtLifeCovRunSecureHelper(t, "stage", []*os.File{descriptor}, []string{secureStageCheckArgument, stage})
	if equal.exitCode != 1 || !strings.Contains(equal.stderr, "outside trusted operation root") {
		t.Fatalf("containment check at root exit = %d, stderr = %q", equal.exitCode, equal.stderr)
	}
}

func TestWtLifeCovSecureStageHelperRunsGitFromHeldStage(t *testing.T) {
	operationRoot, stage, descriptor := wtLifeCovStage(t)
	marker := filepath.Join(operationRoot, "ran.txt")
	script := wtLifeCovScript(t, "git-stub.sh", "printf 'stub-output\\n'\nprintf 'ran' > \"$WT_LIFECOV_MARKER\"\nexit 0\n")
	result := wtLifeCovRunSecureHelper(t, "stage", []*os.File{descriptor},
		[]string{operationRoot, script, "ignored-argument"},
		"WT_LIFECOV_MARKER="+marker)
	if result.exitCode != 0 {
		t.Fatalf("stage handoff exit = %d, stderr = %q", result.exitCode, result.stderr)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("stage handoff did not run the handed command: %v", err)
	}
	if !strings.Contains(result.stderr, "stub-output") {
		t.Fatalf("stage handoff stderr = %q, want the command output", result.stderr)
	}
	_ = stage

	// A call whose first argument is the trusted root itself is outside that
	// root, so the command must never run.
	unrun := filepath.Join(operationRoot, "never.txt")
	blocked := wtLifeCovRunSecureHelper(t, "stage", []*os.File{descriptor},
		[]string{stage, script, "ignored-argument"},
		"WT_LIFECOV_MARKER="+unrun)
	if blocked.exitCode != 1 || !strings.Contains(blocked.stderr, "outside trusted operation root") {
		t.Fatalf("escaped stage handoff exit = %d, stderr = %q", blocked.exitCode, blocked.stderr)
	}
	if _, err := os.Stat(unrun); !os.IsNotExist(err) {
		t.Fatalf("escaped stage handoff ran its command: %v", err)
	}
}

func TestWtLifeCovSecureStageHelperPropagatesCommandFailure(t *testing.T) {
	operationRoot, _, descriptor := wtLifeCovStage(t)

	failing := wtLifeCovScript(t, "git-stub.sh", "printf 'boom\\n' >&2\nexit 7\n")
	result := wtLifeCovRunSecureHelper(t, "stage", []*os.File{descriptor},
		[]string{operationRoot, failing, "ignored"})
	if result.exitCode != 7 {
		t.Fatalf("failing handoff exit = %d, want 7 (stderr %q)", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stderr, "boom") {
		t.Fatalf("failing handoff stderr = %q, want the command output", result.stderr)
	}

	missing := filepath.Join(operationRoot, "missing-executable")
	unrunnable := wtLifeCovRunSecureHelper(t, "stage", []*os.File{descriptor},
		[]string{operationRoot, missing, "ignored"})
	if unrunnable.exitCode != 1 || !strings.Contains(unrunnable.stderr, "run git:") {
		t.Fatalf("unrunnable handoff exit = %d, stderr = %q", unrunnable.exitCode, unrunnable.stderr)
	}
}

// ---------------------------------------------------------------------------
// RunSecureStageCanonicalGitHelper
// ---------------------------------------------------------------------------

func TestWtLifeCovSecureStageCanonicalHelperRejectsInvalidArguments(t *testing.T) {
	operationRoot, stage, descriptor := wtLifeCovStage(t)
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	files := []*os.File{descriptor, repo.root, repo.common}

	cases := []struct {
		name string
		args []string
	}{
		{name: "too few arguments", args: []string{operationRoot, repo.path, git, "feature", repo.head}},
		{name: "relative stage", args: []string{"relative", repo.path, git, "feature", repo.head, "0"}},
		{name: "relative canonical", args: []string{operationRoot, "relative", git, "feature", repo.head, "0"}},
		{name: "invalid branch-exists flag", args: []string{operationRoot, repo.path, git, "feature", repo.head, "2"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result := wtLifeCovRunSecureHelper(t, "stage-canonical", files, testCase.args)
			if result.exitCode != 1 || !strings.Contains(result.stderr, "invalid arguments") {
				t.Fatalf("exit = %d, stderr = %q", result.exitCode, result.stderr)
			}
		})
	}
	_ = stage
}

func TestWtLifeCovSecureStageCanonicalHelperRejectsDescriptorAndContainmentDrift(t *testing.T) {
	operationRoot, stage, descriptor := wtLifeCovStage(t)
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	other := wtLifeCovOpenDirectory(t, t.TempDir())

	missing := wtLifeCovRunSecureHelper(t, "stage-canonical", nil,
		[]string{operationRoot, repo.path, git, "feature", repo.head, "0"})
	if missing.exitCode != 1 || !strings.Contains(missing.stderr, "enter inherited stage") {
		t.Fatalf("missing descriptors exit = %d, stderr = %q", missing.exitCode, missing.stderr)
	}

	escaped := wtLifeCovRunSecureHelper(t, "stage-canonical",
		[]*os.File{descriptor, repo.root, repo.common},
		[]string{stage, repo.path, git, "feature", repo.head, "0"})
	if escaped.exitCode != 1 || !strings.Contains(escaped.stderr, "outside trusted operation root") {
		t.Fatalf("escaped stage exit = %d, stderr = %q", escaped.exitCode, escaped.stderr)
	}

	regular := filepath.Join(operationRoot, "not-a-directory")
	if err := os.WriteFile(regular, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileHandle, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fileHandle.Close() }()
	notDirectory := wtLifeCovRunSecureHelper(t, "stage-canonical",
		[]*os.File{descriptor, fileHandle, repo.common},
		[]string{operationRoot, repo.path, git, "feature", repo.head, "0"})
	if notDirectory.exitCode != 1 || !strings.Contains(notDirectory.stderr, "enter inherited canonical root") {
		t.Fatalf("non-directory canonical exit = %d, stderr = %q", notDirectory.exitCode, notDirectory.stderr)
	}

	pathMismatch := wtLifeCovRunSecureHelper(t, "stage-canonical",
		[]*os.File{descriptor, repo.root, repo.common},
		[]string{operationRoot, other.Name(), git, "feature", repo.head, "0"})
	if pathMismatch.exitCode != 1 || !strings.Contains(pathMismatch.stderr, "canonical Git directory changed") {
		t.Fatalf("canonical path mismatch exit = %d, stderr = %q", pathMismatch.exitCode, pathMismatch.stderr)
	}

	entryMismatch := wtLifeCovRunSecureHelper(t, "stage-canonical",
		[]*os.File{descriptor, repo.root, other},
		[]string{operationRoot, repo.path, git, "feature", repo.head, "0"})
	if entryMismatch.exitCode != 1 || !strings.Contains(entryMismatch.stderr, "canonical Git directory changed") {
		t.Fatalf("canonical entry mismatch exit = %d, stderr = %q", entryMismatch.exitCode, entryMismatch.stderr)
	}
}

func TestWtLifeCovSecureStageCanonicalHelperReportsHookLayoutFailure(t *testing.T) {
	operationRoot, _, descriptor := wtLifeCovStage(t)
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	result := wtLifeCovRunSecureHelperInvocation(t, wtLifeCovHelperInvocation{
		helper: "stage-canonical",
		files:  []*os.File{descriptor, repo.root, repo.common},
		args:   []string{operationRoot, repo.path, git, "feature", repo.head, "0"},
		drop:   []string{"WB_HOME", "HOME", "XDG_DATA_HOME"},
	})
	if result.exitCode != 1 || !strings.Contains(result.stderr, "prepare hook runtime layout") {
		t.Fatalf("hook layout failure exit = %d, stderr = %q", result.exitCode, result.stderr)
	}
}

func TestWtLifeCovSecureStageCanonicalHelperCreatesStagedCheckout(t *testing.T) {
	operationRoot, stage, descriptor := wtLifeCovStage(t)
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	const branch = "wtlifecov-staged"
	result := wtLifeCovRunSecureHelper(t, "stage-canonical",
		[]*os.File{descriptor, repo.root, repo.common},
		[]string{operationRoot, repo.path, git, branch, repo.head, "0"})
	if result.exitCode != 0 {
		t.Fatalf("staged checkout exit = %d, stdout = %q, stderr = %q", result.exitCode, result.stdout, result.stderr)
	}
	checkout := filepath.Join(stage, "checkout")
	if _, err := os.Stat(filepath.Join(checkout, "README.md")); err != nil {
		t.Fatalf("staged checkout missing README: %v", err)
	}
	if current := strings.TrimSpace(wtLifeCovGit(t, checkout, "branch", "--show-current")); current != branch {
		t.Fatalf("staged checkout branch = %q, want %q", current, branch)
	}
	if tip := strings.TrimSpace(wtLifeCovGit(t, checkout, "rev-parse", "HEAD")); tip != repo.head {
		t.Fatalf("staged checkout HEAD = %q, want %q", tip, repo.head)
	}
}

func TestWtLifeCovSecureStageCanonicalHelperSurfacesExecFailure(t *testing.T) {
	operationRoot, _, descriptor := wtLifeCovStage(t)
	repo := wtLifeCovNewRepo(t)
	broken := wtLifeCovNonExecutable(t, operationRoot)
	result := wtLifeCovRunSecureHelper(t, "stage-canonical",
		[]*os.File{descriptor, repo.root, repo.common},
		[]string{operationRoot, repo.path, broken, "feature", repo.head, "0"})
	if result.exitCode != 1 {
		t.Fatalf("exec failure exit = %d, want 1 (stderr %q)", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stderr, "Git") {
		t.Fatalf("exec failure stderr = %q, want a capability diagnostic", result.stderr)
	}
}

// ---------------------------------------------------------------------------
// RunSecureCanonicalGitHelper
// ---------------------------------------------------------------------------

func TestWtLifeCovSecureCanonicalHelperRejectsInvalidArguments(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	for _, args := range [][]string{
		{repo.path},
		{"relative", git},
	} {
		result := wtLifeCovRunSecureHelper(t, "canonical", []*os.File{repo.root, repo.common}, args)
		if result.exitCode != 1 || !strings.Contains(result.stderr, "missing Git executable") {
			t.Fatalf("args %v exit = %d, stderr = %q", args, result.exitCode, result.stderr)
		}
	}
}

func TestWtLifeCovSecureCanonicalHelperRejectsDescriptorDrift(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	other := wtLifeCovOpenDirectory(t, t.TempDir())

	missing := wtLifeCovRunSecureHelper(t, "canonical", nil, []string{repo.path, git, "rev-parse", "HEAD"})
	if missing.exitCode != 1 || !strings.Contains(missing.stderr, "enter inherited canonical root") {
		t.Fatalf("missing descriptors exit = %d, stderr = %q", missing.exitCode, missing.stderr)
	}

	entryMismatch := wtLifeCovRunSecureHelper(t, "canonical",
		[]*os.File{repo.root, other}, []string{repo.path, git, "rev-parse", "HEAD"})
	if entryMismatch.exitCode != 1 || !strings.Contains(entryMismatch.stderr, "canonical Git directory changed before Git operation") {
		t.Fatalf("entry mismatch exit = %d, stderr = %q", entryMismatch.exitCode, entryMismatch.stderr)
	}

	pathMismatch := wtLifeCovRunSecureHelper(t, "canonical",
		[]*os.File{repo.root, repo.common}, []string{other.Name(), git, "rev-parse", "HEAD"})
	if pathMismatch.exitCode != 1 || !strings.Contains(pathMismatch.stderr, "canonical repository path changed before Git operation") {
		t.Fatalf("path mismatch exit = %d, stderr = %q", pathMismatch.exitCode, pathMismatch.stderr)
	}
}

func TestWtLifeCovSecureCanonicalHelperReportsHookLayoutFailure(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	result := wtLifeCovRunSecureHelperInvocation(t, wtLifeCovHelperInvocation{
		helper: "canonical",
		files:  []*os.File{repo.root, repo.common},
		args:   []string{repo.path, git, "rev-parse", "HEAD"},
		drop:   []string{"WB_HOME", "HOME", "XDG_DATA_HOME"},
	})
	if result.exitCode != 1 || !strings.Contains(result.stderr, "prepare hook runtime layout") {
		t.Fatalf("hook layout failure exit = %d, stderr = %q", result.exitCode, result.stderr)
	}
}

func TestWtLifeCovSecureCanonicalHelperRunsGitFromHeldRoot(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	result := wtLifeCovRunSecureHelper(t, "canonical",
		[]*os.File{repo.root, repo.common}, []string{repo.path, git, "rev-parse", "HEAD"})
	if result.exitCode != 0 {
		t.Fatalf("canonical git exit = %d, stdout = %q, stderr = %q", result.exitCode, result.stdout, result.stderr)
	}
	if got := strings.TrimSpace(wtLifeCovSection(result.stdout)); got != repo.head {
		t.Fatalf("canonical git printed %q, want %q", got, repo.head)
	}
}

func TestWtLifeCovSecureCanonicalHelperSurfacesExecFailure(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	broken := wtLifeCovNonExecutable(t, t.TempDir())
	result := wtLifeCovRunSecureHelper(t, "canonical",
		[]*os.File{repo.root, repo.common}, []string{repo.path, broken, "rev-parse", "HEAD"})
	if result.exitCode != 1 {
		t.Fatalf("exec failure exit = %d, want 1 (stderr %q)", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stderr, "Git") {
		t.Fatalf("exec failure stderr = %q, want a capability diagnostic", result.stderr)
	}
}

// ---------------------------------------------------------------------------
// RunSecureCanonicalPolicyGitHelper
// ---------------------------------------------------------------------------

func TestWtLifeCovSecureCanonicalPolicyHelperRejectsNonPolicyQueries(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	cases := []struct {
		name string
		args []string
	}{
		{name: "too few arguments", args: []string{repo.path, git}},
		{name: "relative canonical root", args: []string{"relative", git, "show", repo.head}},
		{name: "disallowed verb", args: []string{repo.path, git, "status"}},
		{name: "cat-file with extra argument", args: []string{repo.path, git, "cat-file", "-s", repo.head, "extra"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result := wtLifeCovRunSecureHelper(t, "canonical-policy",
				[]*os.File{repo.root, repo.common}, testCase.args)
			if result.exitCode != 1 || !strings.Contains(result.stderr, "invalid read-only query") {
				t.Fatalf("exit = %d, stderr = %q", result.exitCode, result.stderr)
			}
		})
	}
}

func TestWtLifeCovSecureCanonicalPolicyHelperRejectsDescriptorDrift(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	other := wtLifeCovOpenDirectory(t, t.TempDir())

	missing := wtLifeCovRunSecureHelper(t, "canonical-policy", nil, []string{repo.path, git, "show", repo.head})
	if missing.exitCode != 1 || !strings.Contains(missing.stderr, "canonical Git directory changed before policy read") {
		t.Fatalf("missing descriptors exit = %d, stderr = %q", missing.exitCode, missing.stderr)
	}

	entryMismatch := wtLifeCovRunSecureHelper(t, "canonical-policy",
		[]*os.File{repo.root, other}, []string{repo.path, git, "show", repo.head})
	if entryMismatch.exitCode != 1 || !strings.Contains(entryMismatch.stderr, "canonical Git directory changed before policy read") {
		t.Fatalf("entry mismatch exit = %d, stderr = %q", entryMismatch.exitCode, entryMismatch.stderr)
	}

	pathMismatch := wtLifeCovRunSecureHelper(t, "canonical-policy",
		[]*os.File{repo.root, repo.common}, []string{other.Name(), git, "show", repo.head})
	if pathMismatch.exitCode != 1 || !strings.Contains(pathMismatch.stderr, "canonical repository path changed before policy read") {
		t.Fatalf("path mismatch exit = %d, stderr = %q", pathMismatch.exitCode, pathMismatch.stderr)
	}
}

func TestWtLifeCovSecureCanonicalPolicyHelperReadsExactPolicyBytes(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	result := wtLifeCovRunSecureHelper(t, "canonical-policy",
		[]*os.File{repo.root, repo.common}, []string{repo.path, git, "show", repo.head})
	if result.exitCode != 0 {
		t.Fatalf("policy read exit = %d, stderr = %q", result.exitCode, result.stderr)
	}
	section := wtLifeCovSection(result.stdout)
	if !strings.Contains(section, "initial") || !strings.Contains(section, "README.md") {
		t.Fatalf("policy read stdout = %q, want the committed tree", section)
	}
}

func TestWtLifeCovSecureCanonicalPolicyHelperReportsGitAndExecFailures(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)

	failed := wtLifeCovRunSecureHelper(t, "canonical-policy",
		[]*os.File{repo.root, repo.common},
		[]string{repo.path, git, "show", strings.Repeat("0", 40)})
	if failed.exitCode != 1 || strings.TrimSpace(failed.stderr) == "" {
		t.Fatalf("failing policy query exit = %d, stderr = %q", failed.exitCode, failed.stderr)
	}

	broken := filepath.Join(repo.path, "missing-git")
	unexecutable := wtLifeCovRunSecureHelper(t, "canonical-policy",
		[]*os.File{repo.root, repo.common}, []string{repo.path, broken, "show", repo.head})
	if unexecutable.exitCode != 1 || strings.TrimSpace(unexecutable.stderr) == "" {
		t.Fatalf("unrunnable policy query exit = %d, stderr = %q", unexecutable.exitCode, unexecutable.stderr)
	}
}

func TestWtLifeCovSecureCanonicalPolicyHelperRejectsRepositoryChangedDuringRead(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	moved := repo.path + "-git-moved"
	script := wtLifeCovScript(t, "policy-read-stub.sh", "mv \"$WT_LIFECOV_REPO/.git\" \"$WT_LIFECOV_MOVED\"\nprintf 'policy bytes'\n")
	result := wtLifeCovRunSecureHelper(t, "canonical-policy",
		[]*os.File{repo.root, repo.common},
		[]string{repo.path, script, "show", repo.head},
		"WT_LIFECOV_REPO="+repo.path,
		"WT_LIFECOV_MOVED="+moved)
	if result.exitCode != 1 || !strings.Contains(result.stderr, "canonical repository changed during policy read") {
		t.Fatalf("changed-during-read exit = %d, stderr = %q", result.exitCode, result.stderr)
	}
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("policy read stub did not move the Git directory: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RunSecureCleanupGitHelper
// ---------------------------------------------------------------------------

func wtLifeCovCleanupArgs(repo *wtLifeCovRepo, git, worktreePath, worktreeParentPath, remotePath, remoteFD string, gitArgs ...string) []string {
	return append([]string{
		repo.path, worktreePath, worktreeParentPath, git, remotePath, remoteFD,
	}, gitArgs...)
}

func TestWtLifeCovSecureCleanupHelperRejectsMalformedRequests(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	result := wtLifeCovRunSecureHelper(t, "cleanup",
		[]*os.File{repo.root, repo.common},
		[]string{repo.path, "", "", git, "", "-1"})
	if result.exitCode != 1 || !strings.Contains(result.stderr, "missing worktree path or Git command") {
		t.Fatalf("short cleanup request exit = %d, stderr = %q", result.exitCode, result.stderr)
	}
}

func TestWtLifeCovSecureCleanupHelperRejectsDescriptorDrift(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	other := wtLifeCovOpenDirectory(t, t.TempDir())
	args := wtLifeCovCleanupArgs(repo, git, "", "", "", "-1", "rev-parse", "HEAD")

	missing := wtLifeCovRunSecureHelper(t, "cleanup", nil, args)
	if missing.exitCode != 1 || !strings.Contains(missing.stderr, "enter inherited canonical repository") {
		t.Fatalf("missing descriptors exit = %d, stderr = %q", missing.exitCode, missing.stderr)
	}

	entryMismatch := wtLifeCovRunSecureHelper(t, "cleanup", []*os.File{repo.root, other}, args)
	if entryMismatch.exitCode != 1 || !strings.Contains(entryMismatch.stderr, "canonical repository path changed before Git operation") {
		t.Fatalf("entry mismatch exit = %d, stderr = %q", entryMismatch.exitCode, entryMismatch.stderr)
	}

	mismatched := wtLifeCovRunSecureHelper(t, "cleanup", []*os.File{repo.root, repo.common},
		[]string{other.Name(), "", "", git, "", "-1", "rev-parse", "HEAD"})
	if mismatched.exitCode != 1 || !strings.Contains(mismatched.stderr, "canonical repository path changed before Git operation") {
		t.Fatalf("path mismatch exit = %d, stderr = %q", mismatched.exitCode, mismatched.stderr)
	}
}

func TestWtLifeCovSecureCleanupHelperRunsGitFromHeldCanonical(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	result := wtLifeCovRunSecureHelper(t, "cleanup",
		[]*os.File{repo.root, repo.common},
		wtLifeCovCleanupArgs(repo, git, "", "", "", "-1", "rev-parse", "HEAD"))
	if result.exitCode != 0 {
		t.Fatalf("cleanup git exit = %d, stdout = %q, stderr = %q", result.exitCode, result.stdout, result.stderr)
	}
	if got := strings.TrimSpace(wtLifeCovSection(result.stdout)); got != repo.head {
		t.Fatalf("cleanup git printed %q, want %q", got, repo.head)
	}
}

func TestWtLifeCovSecureCleanupHelperValidatesHeldWorktree(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	parent := t.TempDir()
	worktree := filepath.Join(parent, "task")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	parentDescriptor := wtLifeCovOpenDirectory(t, parent)
	worktreeDescriptor := wtLifeCovOpenDirectory(t, worktree)
	other := wtLifeCovOpenDirectory(t, t.TempDir())

	args := wtLifeCovCleanupArgs(repo, git, worktree, parent, "", "-1", "rev-parse", "HEAD")
	ok := wtLifeCovRunSecureHelper(t, "cleanup",
		[]*os.File{repo.root, repo.common, parentDescriptor, worktreeDescriptor}, args)
	if ok.exitCode != 0 {
		t.Fatalf("held worktree cleanup exit = %d, stderr = %q", ok.exitCode, ok.stderr)
	}

	mismatched := wtLifeCovRunSecureHelper(t, "cleanup",
		[]*os.File{repo.root, repo.common, parentDescriptor, other}, args)
	if mismatched.exitCode != 1 || !strings.Contains(mismatched.stderr, "worktree path changed before Git operation") {
		t.Fatalf("worktree mismatch exit = %d, stderr = %q", mismatched.exitCode, mismatched.stderr)
	}

	parentMismatch := wtLifeCovRunSecureHelper(t, "cleanup",
		[]*os.File{repo.root, repo.common, other, worktreeDescriptor}, args)
	if parentMismatch.exitCode != 1 || !strings.Contains(parentMismatch.stderr, "worktree path changed before Git operation") {
		t.Fatalf("worktree parent mismatch exit = %d, stderr = %q", parentMismatch.exitCode, parentMismatch.stderr)
	}
}

// TestWtLifeCovSecureCleanupHelperAuthorizesEveryHeldRoot exercises the
// capability construction for the worktree-parent and local-remote roots. A
// non-executable Git keeps the helper from replacing its own process at exec,
// so those authorizations are observable and the refusal is asserted.
func TestWtLifeCovSecureCleanupHelperAuthorizesEveryHeldRoot(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	broken := wtLifeCovNonExecutable(t, t.TempDir())

	parent := t.TempDir()
	worktree := filepath.Join(parent, "task")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	parentDescriptor := wtLifeCovOpenDirectory(t, parent)
	worktreeDescriptor := wtLifeCovOpenDirectory(t, worktree)
	withWorktree := wtLifeCovRunSecureHelper(t, "cleanup",
		[]*os.File{repo.root, repo.common, parentDescriptor, worktreeDescriptor},
		wtLifeCovCleanupArgs(repo, broken, worktree, parent, "", "-1", "rev-parse", "HEAD"))
	if withWorktree.exitCode != 1 {
		t.Fatalf("worktree-authorized cleanup exit = %d, stderr = %q", withWorktree.exitCode, withWorktree.stderr)
	}

	remote := filepath.Join(t.TempDir(), "remote.git")
	wtLifeCovGit(t, t.TempDir(), "init", "--bare", "--quiet", remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	remoteDescriptor := wtLifeCovOpenDirectory(t, remote)
	withRemote := wtLifeCovRunSecureHelper(t, "cleanup",
		[]*os.File{repo.root, repo.common, remoteDescriptor},
		wtLifeCovCleanupArgs(repo, broken, "", "", remote, "5", "rev-parse", "HEAD"))
	if withRemote.exitCode != 1 {
		t.Fatalf("remote-authorized cleanup exit = %d, stderr = %q", withRemote.exitCode, withRemote.stderr)
	}
}

func TestWtLifeCovSecureCleanupHelperValidatesLocalRemoteDescriptor(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	remote := t.TempDir()
	remoteDescriptor := wtLifeCovOpenDirectory(t, remote)

	malformed := wtLifeCovRunSecureHelper(t, "cleanup",
		[]*os.File{repo.root, repo.common, remoteDescriptor},
		wtLifeCovCleanupArgs(repo, git, "", "", remote, "not-a-number", "rev-parse", "HEAD"))
	if malformed.exitCode != 1 || !strings.Contains(malformed.stderr, "invalid local remote descriptor") {
		t.Fatalf("malformed remote descriptor exit = %d, stderr = %q", malformed.exitCode, malformed.stderr)
	}

	tooLow := wtLifeCovRunSecureHelper(t, "cleanup",
		[]*os.File{repo.root, repo.common, remoteDescriptor},
		wtLifeCovCleanupArgs(repo, git, "", "", remote, "4", "rev-parse", "HEAD"))
	if tooLow.exitCode != 1 || !strings.Contains(tooLow.stderr, "invalid local remote descriptor") {
		t.Fatalf("low remote descriptor exit = %d, stderr = %q", tooLow.exitCode, tooLow.stderr)
	}

	mismatch := wtLifeCovRunSecureHelper(t, "cleanup",
		[]*os.File{repo.root, repo.common, remoteDescriptor},
		wtLifeCovCleanupArgs(repo, git, "", "", remote+"-other", "5", "rev-parse", "HEAD"))
	if mismatch.exitCode != 1 || !strings.Contains(mismatch.stderr, "local remote path changed before Git operation") {
		t.Fatalf("remote path mismatch exit = %d, stderr = %q", mismatch.exitCode, mismatch.stderr)
	}
}

func TestWtLifeCovSecureCleanupHelperPushesToHeldLocalRemote(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	wtLifeCovGit(t, t.TempDir(), "init", "--bare", "--quiet", "--initial-branch=main", remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	remoteDescriptor := wtLifeCovOpenDirectory(t, remote)

	result := wtLifeCovRunSecureHelper(t, "cleanup",
		[]*os.File{repo.root, repo.common, remoteDescriptor},
		wtLifeCovCleanupArgs(repo, git, "", "", remote, "5", "push", "--", remote, "HEAD:refs/heads/wtlifecov-pushed"))
	if result.exitCode != 0 {
		t.Fatalf("held remote push exit = %d, stdout = %q, stderr = %q", result.exitCode, result.stdout, result.stderr)
	}
	pushed := strings.TrimSpace(wtLifeCovGit(t, remote, "rev-parse", "refs/heads/wtlifecov-pushed"))
	if pushed != repo.head {
		t.Fatalf("pushed ref = %q, want %q", pushed, repo.head)
	}
}

func TestWtLifeCovSecureCleanupHelperReportsHookLayoutFailure(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	git := wtLifeCovTrustedGit(t)
	result := wtLifeCovRunSecureHelperInvocation(t, wtLifeCovHelperInvocation{
		helper: "cleanup",
		files:  []*os.File{repo.root, repo.common},
		args:   wtLifeCovCleanupArgs(repo, git, "", "", "", "-1", "rev-parse", "HEAD"),
		drop:   []string{"WB_HOME", "HOME", "XDG_DATA_HOME"},
	})
	if result.exitCode != 1 || !strings.Contains(result.stderr, "prepare hook runtime layout") {
		t.Fatalf("hook layout failure exit = %d, stderr = %q", result.exitCode, result.stderr)
	}
}

func TestWtLifeCovSecureCleanupHelperSurfacesExecFailure(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	broken := wtLifeCovNonExecutable(t, t.TempDir())
	result := wtLifeCovRunSecureHelper(t, "cleanup",
		[]*os.File{repo.root, repo.common},
		wtLifeCovCleanupArgs(repo, broken, "", "", "", "-1", "rev-parse", "HEAD"))
	if result.exitCode != 1 {
		t.Fatalf("exec failure exit = %d, want 1 (stderr %q)", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stderr, "Git") {
		t.Fatalf("exec failure stderr = %q, want a capability diagnostic", result.stderr)
	}
}

// ---------------------------------------------------------------------------
// gitCanonicalPolicyEnvironment
// ---------------------------------------------------------------------------

func TestWtLifeCovCanonicalPolicyEnvironmentPinsHeldGitDirectory(t *testing.T) {
	t.Setenv("GIT_DIR", "/ambient/git-dir")
	t.Setenv("GIT_WORK_TREE", "/ambient/work-tree")
	t.Setenv("TMPDIR", "/ambient/tmp")
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	environment := gitCanonicalPolicyEnvironment()
	values := map[string]string{}
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if found {
			values[key] = value
		}
	}
	for key, want := range map[string]string{
		"GIT_DIR":             ".",
		"GIT_WORK_TREE":       "..",
		"GIT_OPTIONAL_LOCKS":  "0",
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   "/dev/null",
	} {
		if values[key] != want {
			t.Fatalf("policy environment %s = %q, want %q", key, values[key], want)
		}
	}
	// The ambient values must not survive anywhere in the child environment.
	for _, entry := range environment {
		if strings.HasPrefix(entry, "GIT_DIR=/ambient") || strings.HasPrefix(entry, "GIT_WORK_TREE=/ambient") || entry == "TMPDIR=/ambient/tmp" {
			t.Fatalf("policy environment leaked ambient entry %q", entry)
		}
	}
}

// ---------------------------------------------------------------------------
// In-process cleanup helper plumbing
// ---------------------------------------------------------------------------

func TestWtLifeCovRunSecureCleanupGitHelperRejectsUnusableCanonical(t *testing.T) {
	err := runSecureCleanupGitHelper(context.Background(), nil, nil, nil, "", "")
	if err == nil || !strings.Contains(err.Error(), "cleanup canonical repository descriptor is unavailable") {
		t.Fatalf("nil canonical error = %v", err)
	}
	empty := &canonicalRepository{path: t.TempDir()}
	if err := runSecureCleanupGitHelper(context.Background(), empty, nil, nil, "", ""); err == nil || !strings.Contains(err.Error(), "cleanup canonical repository descriptor is unavailable") {
		t.Fatalf("descriptorless canonical error = %v", err)
	}
}

func TestWtLifeCovRunSecureCleanupGitHelperReportsCanonicalDrift(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	canonical, err := openCanonicalRepository(repo.path)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	moved := repo.path + "-moved"
	if renameErr := os.Rename(repo.path, moved); renameErr != nil {
		t.Fatalf("move canonical repository: %v", renameErr)
	}
	err = runSecureCleanupGitHelper(context.Background(), canonical, nil, nil, "", "", "rev-parse", "HEAD")
	if err == nil || !strings.Contains(err.Error(), "canonical repository path changed before Git operation") {
		t.Fatalf("canonical drift error = %v", err)
	}
}

func TestWtLifeCovRunSecureCleanupGitHelperRequiresWorktreeParent(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	canonical, err := openCanonicalRepository(repo.path)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	worktree := wtLifeCovOpenDirectory(t, t.TempDir())
	err = runSecureCleanupGitHelper(t.Context(), canonical, nil, worktree, "", repo.path, "rev-parse", "HEAD")
	if err == nil || !strings.Contains(err.Error(), "cleanup worktree parent descriptor is unavailable") {
		t.Fatalf("missing worktree parent error = %v", err)
	}
}

func TestWtLifeCovRunSecureCleanupGitHelperReportsChildFailure(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	canonical, err := openCanonicalRepository(repo.path)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	err = runSecureCleanupGitHelper(t.Context(), canonical, nil, nil, "", "", "rev-parse", "--verify", "refs/heads/absent")
	if err == nil || !strings.Contains(err.Error(), "run descriptor-anchored cleanup Git") {
		t.Fatalf("failing cleanup git error = %v", err)
	}
}

func TestWtLifeCovRunSecureCleanupGitHelperRunsFromHeldDescriptors(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	canonical, err := openCanonicalRepository(repo.path)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	if err := runSecureCleanupGitHelper(t.Context(), canonical, nil, nil, "", "", "rev-parse", "HEAD"); err != nil {
		t.Fatalf("cleanup git run: %v", err)
	}
}

func TestWtLifeCovLocalOriginDirectoryForSecurePushClassifiesRemotes(t *testing.T) {
	repo := wtLifeCovNewRepo(t)
	canonical, err := openCanonicalRepository(repo.path)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()

	for _, gitArgs := range [][]string{nil, {"fetch", "origin"}} {
		directory, path, err := localOriginDirectoryForSecurePush(t.Context(), canonical, gitArgs)
		if err != nil || directory != nil || path != "" {
			t.Fatalf("non-push %v = (%v, %q, %v), want no remote root", gitArgs, directory, path, err)
		}
	}

	remote := filepath.Join(t.TempDir(), "remote.git")
	wtLifeCovGit(t, t.TempDir(), "init", "--bare", "--quiet", remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	directory, path, err := localOriginDirectoryForSecurePush(t.Context(), canonical, []string{"push", "--", remote, "HEAD:refs/heads/x"})
	if err != nil {
		t.Fatalf("local push remote: %v", err)
	}
	if directory == nil || path == "" {
		t.Fatalf("local push remote = (%v, %q), want a held directory", directory, path)
	}
	_ = directory.Close()

	network, networkPath, err := localOriginDirectoryForSecurePush(t.Context(), canonical, []string{"push", "git@example.test:acme/app.git", "HEAD"})
	if err != nil || network != nil || networkPath != "" {
		t.Fatalf("network push remote = (%v, %q, %v), want none", network, networkPath, err)
	}

	if _, _, err := localOriginDirectoryForSecurePush(t.Context(), canonical, []string{"push"}); err == nil {
		t.Fatal("push without a repository argument should fail")
	}
	if _, _, err := localOriginDirectoryForSecurePush(t.Context(), canonical, []string{"push", "--"}); err == nil {
		t.Fatal("push with a dangling separator should fail")
	}
	if _, _, err := localOriginDirectoryForSecurePush(t.Context(), canonical, []string{"push", "origin"}); err == nil ||
		!strings.Contains(err.Error(), "resolve configured push URL") {
		t.Fatalf("unconfigured origin push error = %v", err)
	}
}

func TestWtLifeCovSecurePushRepositoryArgumentSelectsRemote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args []string
		want string
	}{
		{args: []string{"push", "origin", "main"}, want: "origin"},
		{args: []string{"push", "--force", "origin", "main"}, want: "origin"},
		{args: []string{"push", "--", "origin", "main"}, want: "origin"},
	}
	for _, testCase := range cases {
		got, err := securePushRepositoryArgument(testCase.args)
		if err != nil || got != testCase.want {
			t.Fatalf("securePushRepositoryArgument(%v) = (%q, %v), want %q", testCase.args, got, err, testCase.want)
		}
	}
	for _, args := range [][]string{{"push"}, {"push", "--"}, {"push", "-f"}} {
		if _, err := securePushRepositoryArgument(args); err == nil {
			t.Fatalf("securePushRepositoryArgument(%v) should fail", args)
		}
	}
}
