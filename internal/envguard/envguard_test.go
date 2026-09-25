package envguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestSanitizeEnvOverrideWinsOverAmbientDuplicate(t *testing.T) {
	t.Parallel()
	base := []string{"GOWORK=/ambient/go.work", "PATH=/bin", "HOME=/home/alex"}
	result := SanitizeEnv(base, "GOWORK=off")

	values := envValues(result)
	if values["GOWORK"] != "off" {
		t.Fatalf("GOWORK = %q, want %q (result = %v)", values["GOWORK"], "off", result)
	}
	// Exactly one GOWORK entry: a naive append(base, override...) would leave
	// two, and most C libraries' getenv would then resolve the FIRST --
	// the stale ambient value -- silently ignoring the override.
	count := 0
	for _, entry := range result {
		if strings.HasPrefix(entry, "GOWORK=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("GOWORK appeared %d times in %v, want exactly once", count, result)
	}
	if values["PATH"] != "/bin" || values["HOME"] != "/home/alex" {
		t.Fatalf("unrelated entries were altered: %v", result)
	}
}

func TestSanitizeEnvStripsAgentVarsFromBaseButNotFromOverrides(t *testing.T) {
	t.Parallel()
	base := []string{"WB_AGENT_ID=session-123", "WB_AGENT_PID=42", "PATH=/bin"}
	result := SanitizeEnv(base)

	values := envValues(result)
	if _, present := values["WB_AGENT_ID"]; present {
		t.Fatalf("WB_AGENT_ID leaked through from base: %v", result)
	}
	if _, present := values["WB_AGENT_PID"]; present {
		t.Fatalf("WB_AGENT_PID leaked through from base: %v", result)
	}
	if values["PATH"] != "/bin" {
		t.Fatalf("PATH missing or altered: %v", result)
	}

	// An explicit override is the caller's own intentional value and is
	// never stripped -- a test that deliberately exercises agent-mode
	// behavior must be able to set one.
	withOverride := SanitizeEnv(base, "WB_AGENT_ID=intentional")
	if envValues(withOverride)["WB_AGENT_ID"] != "intentional" {
		t.Fatalf("explicit WB_AGENT_ID override was stripped: %v", withOverride)
	}
}

func TestSanitizeEnvPreservesFirstSeenOrderForStableOutput(t *testing.T) {
	t.Parallel()
	base := []string{"A=1", "B=2", "C=3"}
	result := SanitizeEnv(base, "B=20", "D=4")
	want := []string{"A=1", "B=20", "C=3", "D=4"}
	if len(result) != len(want) {
		t.Fatalf("result = %v, want %v", result, want)
	}
	for index, entry := range want {
		if result[index] != entry {
			t.Fatalf("result = %v, want %v", result, want)
		}
	}
}

func TestInspectFindsGoWorkAncestorsGoworkAndAgentVars(t *testing.T) {
	t.Parallel()
	root := evalSymlinksTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"GOWORK=/some/path", "WB_AGENT_ID=s1", "WB_AGENT_PID=9", "PATH=/bin"}

	inputs := Inspect(env, nested)
	if len(inputs.GoWorkAncestors) != 1 || inputs.GoWorkAncestors[0] != filepath.Join(root, "go.work") {
		t.Fatalf("GoWorkAncestors = %v", inputs.GoWorkAncestors)
	}
	if inputs.GOWORK != "/some/path" {
		t.Fatalf("GOWORK = %q", inputs.GOWORK)
	}
	wantAgentVars := []string{"WB_AGENT_ID", "WB_AGENT_PID"}
	sort.Strings(wantAgentVars)
	if len(inputs.AgentVars) != len(wantAgentVars) {
		t.Fatalf("AgentVars = %v, want %v", inputs.AgentVars, wantAgentVars)
	}
	for index, name := range wantAgentVars {
		if inputs.AgentVars[index] != name {
			t.Fatalf("AgentVars = %v, want %v", inputs.AgentVars, wantAgentVars)
		}
	}
	if inputs.Empty() {
		t.Fatal("Empty() = true, want false")
	}
	if !strings.Contains(inputs.String(), "WB_AGENT_ID") || !strings.Contains(inputs.String(), "go.work") {
		t.Fatalf("String() = %q", inputs.String())
	}
}

func TestInspectEmptyWhenNothingObserved(t *testing.T) {
	t.Parallel()
	root := evalSymlinksTempDir(t) // no go.work anywhere in this fresh directory
	inputs := Inspect([]string{"PATH=/bin", "HOME=/home"}, root)
	if !inputs.Empty() {
		t.Fatalf("inputs = %+v, want Empty", inputs)
	}
	if inputs.String() != "" {
		t.Fatalf("String() = %q, want empty", inputs.String())
	}
}

func TestGoEnvOverridesOffByDefaultAndOffOnError(t *testing.T) {
	t.Parallel()
	root := evalSymlinksTempDir(t)
	// No go.work anywhere in this ancestry: always GOWORK=off.
	overrides := GoEnvOverrides(root)
	if len(overrides) != 1 || overrides[0] != "GOWORK=off" {
		t.Fatalf("overrides = %v, want [GOWORK=off]", overrides)
	}
}

// gitTopLevelArgv, gitCatFileArgv and gitDiffArgv build the exact argv the
// production helpers pass to the runner, so a script matches by argv
// rather than by a loose predicate -- proving the seam sends what real git
// would need, not just "some call happened".

func gitTopLevelArgv(dir string) []string {
	return []string{"git", "-C", dir, "rev-parse", "--show-toplevel"}
}

func gitCatFileArgv(repoRoot, path string) []string {
	return []string{"git", "-C", repoRoot, "cat-file", "-e", "HEAD:" + filepath.ToSlash(path)}
}

func gitDiffArgv(repoRoot, path string) []string {
	return []string{"git", "-C", repoRoot, "diff", "--quiet", "HEAD", "--", path}
}

func TestTracksOwnGoWorkTrueOnlyWhenCommittedAndUnchanged(t *testing.T) {
	t.Parallel()
	repository := evalSymlinksTempDir(t)
	if err := os.WriteFile(filepath.Join(repository, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Untracked go.work: `cat-file -e` reports it absent from HEAD (exit 1).
	fake := runnertest.New(t)
	fake.ExpectArgv(gitTopLevelArgv(repository), runner.Result{Stdout: repository}, nil)
	fake.ExpectArgv(gitCatFileArgv(repository, "go.work"), runner.Result{ExitCode: 1}, fmt.Errorf("exit status 1"))
	tracked, err := tracksOwnGoWork(fake, repository)
	if err != nil {
		t.Fatal(err)
	}
	if tracked {
		t.Fatal("tracksOwnGoWork = true for an untracked go.work")
	}

	// Tracked and unchanged: `cat-file -e` and `diff --quiet` both exit 0.
	fake = runnertest.New(t)
	fake.ExpectArgv(gitTopLevelArgv(repository), runner.Result{Stdout: repository}, nil)
	fake.ExpectArgv(gitCatFileArgv(repository, "go.work"), runner.Result{}, nil)
	fake.ExpectArgv(gitDiffArgv(repository, "go.work"), runner.Result{}, nil)
	tracked, err = tracksOwnGoWork(fake, repository)
	if err != nil {
		t.Fatal(err)
	}
	if !tracked {
		t.Fatal("tracksOwnGoWork = false for a committed, unchanged go.work")
	}

	// Dirty working-tree edit: tracked in HEAD, but `diff --quiet` exits 1.
	fake = runnertest.New(t)
	fake.ExpectArgv(gitTopLevelArgv(repository), runner.Result{Stdout: repository}, nil)
	fake.ExpectArgv(gitCatFileArgv(repository, "go.work"), runner.Result{}, nil)
	fake.ExpectArgv(gitDiffArgv(repository, "go.work"), runner.Result{ExitCode: 1}, fmt.Errorf("exit status 1"))
	tracked, err = tracksOwnGoWork(fake, repository)
	if err != nil {
		t.Fatal(err)
	}
	if tracked {
		t.Fatal("tracksOwnGoWork = true for a go.work that differs from HEAD")
	}
}

// TestTracksOwnGoWorkResolvesGitTopLevelAboveGoWorkDirectory covers a
// go.work that does not sit at the git repository's top level -- e.g. a
// committed nested/go.work with the git root one directory above nested/.
// TracksOwnGoWork must resolve the true top level with `git rev-parse
// --show-toplevel` and check nested/go.work relative to it, not assume
// go.work's own directory is the git root.
func TestTracksOwnGoWorkResolvesGitTopLevelAboveGoWorkDirectory(t *testing.T) {
	t.Parallel()
	root := evalSymlinksTempDir(t)
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("nested", "go.work")

	fake := runnertest.New(t)
	fake.ExpectArgv(gitTopLevelArgv(nested), runner.Result{Stdout: root}, nil)
	fake.ExpectArgv(gitCatFileArgv(root, relative), runner.Result{}, nil)
	fake.ExpectArgv(gitDiffArgv(root, relative), runner.Result{}, nil)

	tracked, err := tracksOwnGoWork(fake, nested)
	if err != nil {
		t.Fatal(err)
	}
	if !tracked {
		t.Fatal("tracksOwnGoWork = false for a committed, unchanged nested/go.work with the git root above it")
	}
}

func TestTracksOwnGoWorkResolvesSymlinkedRepositoryRoot(t *testing.T) {
	t.Parallel()
	physical, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(physical, "repository")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(physical, "repository-alias")
	if err := os.Symlink(repository, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// The git calls must name the resolved (physical) repository, not the
	// symlinked alias TracksOwnGoWork was called with.
	fake := runnertest.New(t)
	fake.ExpectArgv(gitTopLevelArgv(repository), runner.Result{Stdout: repository}, nil)
	fake.ExpectArgv(gitCatFileArgv(repository, "go.work"), runner.Result{}, nil)
	fake.ExpectArgv(gitDiffArgv(repository, "go.work"), runner.Result{}, nil)

	tracked, err := tracksOwnGoWork(fake, alias)
	if err != nil {
		t.Fatal(err)
	}
	if !tracked {
		t.Fatal("tracksOwnGoWork = false through a symlinked repository root")
	}
}

// TestTracksOwnGoWorkFalseOutsideAnyGitRepository covers a temp module (a
// go.work with no enclosing git repository at all): goEnvOverrides must
// yield GOWORK=off rather than erroring or panicking when `git rev-parse
// --show-toplevel` finds no repository (empty stdout, non-zero exit).
func TestTracksOwnGoWorkFalseOutsideAnyGitRepository(t *testing.T) {
	t.Parallel()
	root := evalSymlinksTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := runnertest.New(t)
	fake.ExpectArgv(gitTopLevelArgv(root), runner.Result{ExitCode: 128}, fmt.Errorf("exit status 128"))

	tracked, err := tracksOwnGoWork(fake, root)
	if err != nil {
		t.Fatal(err)
	}
	if tracked {
		t.Fatal("tracksOwnGoWork = true for a go.work outside any git repository")
	}
	// goEnvOverrides makes the exact same TracksOwnGoWork call again, so a
	// fresh fake is scripted with the same single expectation.
	fake = runnertest.New(t)
	fake.ExpectArgv(gitTopLevelArgv(root), runner.Result{ExitCode: 128}, fmt.Errorf("exit status 128"))
	overrides := goEnvOverrides(fake, root)
	if len(overrides) != 1 || overrides[0] != "GOWORK=off" {
		t.Fatalf("overrides = %v, want [GOWORK=off]", overrides)
	}
}

// TestTracksOwnGoWorkRelativeGitTopLevelIsReported covers `git rev-parse
// --show-toplevel` reporting something that cannot be made relative to the
// go.work path: filepath.Rel needs both sides absolute or both relative, so
// a relative-looking toplevel against an absolute go.work path is an error
// that must be surfaced, not swallowed into "not tracked".
func TestTracksOwnGoWorkRelativeGitTopLevelIsReported(t *testing.T) {
	t.Parallel()
	root := evalSymlinksTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := runnertest.New(t)
	fake.ExpectArgv(gitTopLevelArgv(root), runner.Result{Stdout: "not-an-absolute-toplevel"}, nil)

	tracked, err := tracksOwnGoWork(fake, root)
	if err == nil {
		t.Fatal("a relative git toplevel was silently accepted")
	}
	if tracked {
		t.Fatal("tracksOwnGoWork = true with a relative git toplevel")
	}
}

// TestTracksOwnGoWorkUnrunnableTopLevelProbeIsReported covers `git
// rev-parse --show-toplevel` itself failing to launch -- ExitCode 0
// alongside a non-nil error -- which must be reported rather than treated
// as "not inside a git repository".
func TestTracksOwnGoWorkUnrunnableTopLevelProbeIsReported(t *testing.T) {
	t.Parallel()
	root := evalSymlinksTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	launchErr := errors.New("exec: \"git\": executable file not found in $PATH")

	fake := runnertest.New(t)
	fake.ExpectArgv(gitTopLevelArgv(root), runner.Result{}, launchErr)

	tracked, err := tracksOwnGoWork(fake, root)
	if !errors.Is(err, launchErr) {
		t.Fatalf("error = %v, want the launch failure surfaced", err)
	}
	if tracked {
		t.Fatal("tracksOwnGoWork = true after an unrunnable top-level probe")
	}
}

// TestTracksOwnGoWorkUnrunnableObjectProbeIsReported covers the git object
// probe (`cat-file -e`) failing to launch at all -- as opposed to exiting
// non-zero, which means "not in HEAD". A launch failure carries ExitCode 0
// alongside a non-nil error, so it must be reported, never converted into a
// clean "not tracked".
func TestTracksOwnGoWorkUnrunnableObjectProbeIsReported(t *testing.T) {
	t.Parallel()
	root := evalSymlinksTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	launchErr := errors.New("exec: \"git\": executable file not found in $PATH")

	fake := runnertest.New(t)
	fake.ExpectArgv(gitTopLevelArgv(root), runner.Result{Stdout: root}, nil)
	fake.ExpectArgv(gitCatFileArgv(root, "go.work"), runner.Result{}, launchErr)

	tracked, err := tracksOwnGoWork(fake, root)
	if !errors.Is(err, launchErr) {
		t.Fatalf("error = %v, want the launch failure surfaced", err)
	}
	if tracked {
		t.Fatal("tracksOwnGoWork = true after an unrunnable object probe")
	}
}

// TestTracksOwnGoWorkUnexpectedDiffFailureIsReported covers `git diff
// --quiet` failing for a reason other than "the file differs" (exit 1). Any
// other status means the comparison never happened, so it is an error --
// not an unchanged file.
func TestTracksOwnGoWorkUnexpectedDiffFailureIsReported(t *testing.T) {
	t.Parallel()
	root := evalSymlinksTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diffErr := fmt.Errorf("exit status 2")

	fake := runnertest.New(t)
	fake.ExpectArgv(gitTopLevelArgv(root), runner.Result{Stdout: root}, nil)
	fake.ExpectArgv(gitCatFileArgv(root, "go.work"), runner.Result{}, nil)
	fake.ExpectArgv(gitDiffArgv(root, "go.work"), runner.Result{ExitCode: 2}, diffErr)

	tracked, err := tracksOwnGoWork(fake, root)
	if !errors.Is(err, diffErr) {
		t.Fatalf("error = %v, want the git diff exit status 2 surfaced", err)
	}
	if tracked {
		t.Fatal("tracksOwnGoWork = true after a failing git diff")
	}
}

// TestGoEnvOverridesKeepsIntrinsicWorkspace covers the one case where the
// check is NOT isolated: a repository that commits its own go.work and
// leaves it unchanged keeps workspace mode, so there is no override at all.
func TestGoEnvOverridesKeepsIntrinsicWorkspace(t *testing.T) {
	t.Parallel()
	root := evalSymlinksTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := runnertest.New(t)
	fake.ExpectArgv(gitTopLevelArgv(root), runner.Result{Stdout: root}, nil)
	fake.ExpectArgv(gitCatFileArgv(root, "go.work"), runner.Result{}, nil)
	fake.ExpectArgv(gitDiffArgv(root, "go.work"), runner.Result{}, nil)

	if overrides := goEnvOverrides(fake, root); len(overrides) != 0 {
		t.Fatalf("goEnvOverrides = %v, want no override for a repository that tracks its own go.work", overrides)
	}
}

// evalSymlinksTempDir returns t.TempDir(), resolved through
// filepath.EvalSymlinks. macOS routes /var through /private/var, so the raw
// t.TempDir() path differs from the path filepath.EvalSymlinks(repoRoot)
// inside tracksOwnGoWork will actually pass to the git runner -- an
// ExpectArgv script built from the raw path would never match.
func evalSymlinksTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func envValues(entries []string) map[string]string {
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}
