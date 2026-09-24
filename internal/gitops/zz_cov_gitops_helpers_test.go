package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// lgCovGitIdentity pins git's identity and configuration to this test's own
// temp directories, so no ambient global config (author identity, commit
// signing, url.<base>.insteadOf rewrites, templates, hooks) leaks into the
// scratch repositories the test builds. Tests may still override any single
// value afterward with t.Setenv.
func lgCovGitIdentity(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(global, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "lgcov")
	t.Setenv("GIT_AUTHOR_EMAIL", "lgcov@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "lgcov")
	t.Setenv("GIT_COMMITTER_EMAIL", "lgcov@example.com")
}

// lgCovWriteFile writes content to dir/name and returns the full path.
func lgCovWriteFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// lgCovSeededClone returns a bare origin holding one seed commit on main, and a
// clone of it that has main checked out, clean, and equal to origin/main.
func lgCovSeededClone(t *testing.T) (origin, clone string) {
	t.Helper()
	lgCovGitIdentity(t)

	origin = t.TempDir()
	gitIn(t, origin, "init", "-q", "--bare", "-b", "main")
	testenv.ConfigureGitAutoMaintenanceOff(t, origin)

	seed := t.TempDir()
	gitIn(t, seed, "init", "-q", "-b", "main")
	lgCovWriteFile(t, seed, "seed.txt", "seed\n")
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-q", "-m", "seed")
	gitIn(t, seed, "remote", "add", "origin", origin)
	gitIn(t, seed, "push", "-q", "origin", "main")

	clone = filepath.Join(t.TempDir(), "clone")
	gitIn(t, t.TempDir(), "clone", "-q", origin, clone)
	return origin, clone
}

// lgCovFakeBinary writes an executable shell script named name into a fresh
// temp directory and prepends that directory to PATH for this test. Shell
// scripts cannot be executed on Windows, so such tests are skipped there.
func lgCovFakeBinary(t *testing.T, name, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a fake " + name + " binary relies on a POSIX shell script")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

// lgCovFakeGit installs a fake git that appends every invocation's arguments to
// a log file and then runs body, which decides the output and exit status. It
// returns the log path so a test can assert how many times git was called.
func lgCovFakeGit(t *testing.T, body string) (logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "git-invocations.log")
	t.Setenv("LGCOV_GIT_LOG", logPath)
	lgCovFakeBinary(t, "git", "printf '%s\\n' \"$*\" >> \"$LGCOV_GIT_LOG\"\n"+body+"\n")
	return logPath
}

// lgCovInvocations returns the recorded non-empty invocation lines.
func lgCovInvocations(t *testing.T, logPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read invocation log %s: %v", logPath, err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// lgCovFakeGh installs a fake gh that records each invocation and makes
// `gh pr create` print url as its last line. The returned log path lets a test
// assert that both the create and the best-effort auto-merge ran, with the
// arguments the caller passed.
func lgCovFakeGh(t *testing.T, url string) (logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "gh-invocations.log")
	t.Setenv("LGCOV_GH_LOG", logPath)
	t.Setenv("LGCOV_GH_URL", url)
	lgCovFakeBinary(t, "gh", "printf '%s\\n' \"$*\" >> \"$LGCOV_GH_LOG\"\n"+
		"case \"$1 $2\" in\n"+
		"  \"pr create\") echo \"warning: creating pull request\"; echo \"$LGCOV_GH_URL\"; exit 0;;\n"+
		"esac\n"+
		"exit 0\n")
	return logPath
}

// lgCovFakeGhFailingCreate records each invocation but fails every `gh pr
// create`, so the error path of openPR can be exercised while the branch push
// still succeeds.
func lgCovFakeGhFailingCreate(t *testing.T) (logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "gh-invocations.log")
	t.Setenv("LGCOV_GH_LOG", logPath)
	lgCovFakeBinary(t, "gh", "printf '%s\\n' \"$*\" >> \"$LGCOV_GH_LOG\"\n"+
		"case \"$1 $2\" in\n"+
		"  \"pr create\") echo 'gh: could not create pull request' >&2; exit 1;;\n"+
		"esac\n"+
		"exit 0\n")
	return logPath
}

// lgCovFakeGitFailingSubcommand installs a git shim that delegates every
// subcommand to the real git but fails the named one, so a test can drive a
// single internal git failure inside an otherwise real git flow (fetch,
// worktree add, commit) instead of stubbing the whole repository.
func lgCovFakeGitFailingSubcommand(t *testing.T, subcommand, message string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	lgCovFakeBinary(t, "git", "case \"$1\" in\n"+
		"  "+subcommand+") echo \""+message+"\" >&2; exit 1;;\n"+
		"esac\n"+
		"exec \""+realGit+"\" \"$@\"\n")
}

// lgCovRejectPushesTo installs a pre-receive hook in the bare origin that
// rejects exactly the named ref, so a direct push to it fails while a push to
// any other branch succeeds. It models a protected default branch.
func lgCovRejectPushesTo(t *testing.T, origin, ref string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pre-receive hooks rely on a POSIX shell script")
	}
	hooks := filepath.Join(origin, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"status=0\n" +
		"while read old new ref; do\n" +
		"  if [ \"$ref\" = \"" + ref + "\" ]; then\n" +
		"    echo \"remote: " + ref + " is protected\" >&2\n" +
		"    status=1\n" +
		"  fi\n" +
		"done\n" +
		"exit $status\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-receive"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}
