package envguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestSanitizeEnvOverrideWinsOverAmbientDuplicate(t *testing.T) {
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
	root := t.TempDir()
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
	root := t.TempDir() // no go.work anywhere in this fresh directory
	inputs := Inspect([]string{"PATH=/bin", "HOME=/home"}, root)
	if !inputs.Empty() {
		t.Fatalf("inputs = %+v, want Empty", inputs)
	}
	if inputs.String() != "" {
		t.Fatalf("String() = %q, want empty", inputs.String())
	}
}

func TestGoEnvOverridesOffByDefaultAndOffOnError(t *testing.T) {
	root := t.TempDir()
	// No go.work anywhere in this ancestry: always GOWORK=off.
	overrides := GoEnvOverrides(root)
	if len(overrides) != 1 || overrides[0] != "GOWORK=off" {
		t.Fatalf("overrides = %v, want [GOWORK=off]", overrides)
	}
}

func TestTracksOwnGoWorkTrueOnlyWhenCommittedAndUnchanged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.email", "test@example.com")
	runGit(t, repository, "config", "user.name", "Test")

	// Untracked go.work: not the repository's own.
	tracked, err := TracksOwnGoWork(repository)
	if err != nil {
		t.Fatal(err)
	}
	if tracked {
		t.Fatal("TracksOwnGoWork = true for an untracked go.work")
	}

	runGit(t, repository, "add", "go.work")
	runGit(t, repository, "commit", "-m", "add go.work")

	tracked, err = TracksOwnGoWork(repository)
	if err != nil {
		t.Fatal(err)
	}
	if !tracked {
		t.Fatal("TracksOwnGoWork = false for a committed, unchanged go.work")
	}

	// Dirty working-tree edit: no longer "its own" until committed again.
	if err := os.WriteFile(filepath.Join(repository, "go.work"), []byte("go 1.26\n\nuse ./extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracked, err = TracksOwnGoWork(repository)
	if err != nil {
		t.Fatal(err)
	}
	if tracked {
		t.Fatal("TracksOwnGoWork = true for a go.work that differs from HEAD")
	}
}

// TestTracksOwnGoWorkResolvesGitTopLevelAboveGoWorkDirectory covers a
// go.work that does not sit at the git repository's top level -- e.g. a
// committed nested/go.work with the git root one directory above nested/.
// TracksOwnGoWork must resolve the true top level with `git rev-parse
// --show-toplevel` and check nested/go.work relative to it, not assume
// go.work's own directory is the git root.
func TestTracksOwnGoWorkResolvesGitTopLevelAboveGoWorkDirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")

	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "nested/go.work")
	runGit(t, root, "commit", "-m", "add nested go.work")

	// Checks run "under nested/" -- repoRoot is the go.work's own
	// directory, one level below the git top level.
	tracked, err := TracksOwnGoWork(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !tracked {
		t.Fatal("TracksOwnGoWork = false for a committed, unchanged nested/go.work with the git root above it")
	}

	// A dirty edit to the nested go.work is no longer "its own" until
	// committed again -- proves the diff check also resolves the correct
	// relative path, not just the existence check.
	if err := os.WriteFile(filepath.Join(nested, "go.work"), []byte("go 1.26\n\nuse ./extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracked, err = TracksOwnGoWork(nested)
	if err != nil {
		t.Fatal(err)
	}
	if tracked {
		t.Fatal("TracksOwnGoWork = true for a nested go.work that differs from HEAD")
	}
}

// TestTracksOwnGoWorkFalseOutsideAnyGitRepository covers a temp module (a
// go.work with no enclosing git repository at all): GoEnvOverrides must
// yield GOWORK=off rather than erroring or panicking when `git rev-parse
// --show-toplevel` finds no repository.
func TestTracksOwnGoWorkFalseOutsideAnyGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracked, err := TracksOwnGoWork(root)
	if err != nil {
		t.Fatal(err)
	}
	if tracked {
		t.Fatal("TracksOwnGoWork = true for a go.work outside any git repository")
	}

	overrides := GoEnvOverrides(root)
	if len(overrides) != 1 || overrides[0] != "GOWORK=off" {
		t.Fatalf("overrides = %v, want [GOWORK=off]", overrides)
	}
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

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
