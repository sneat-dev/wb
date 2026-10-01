package fleet

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

// TestGitEnvironmentIsAnAllowListPlusTheHardeningSettings requires nothing
// from the daemon's environment but PATH, HOME, TMPDIR, LANG and LC_*, and the
// settings that keep Git read-only, local and non-interactive.
func TestGitEnvironmentIsAnAllowListPlusTheHardeningSettings(t *testing.T) {
	t.Parallel()
	parent := []string{
		"PATH=/bin", "HOME=/home/x", "TMPDIR=/tmp", "LANG=C", "LC_ALL=C", "GIT_DIR=/elsewhere", "GIT_WORK_TREE=/w", "GIT_NAMESPACE=n",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=/bin/evil", "GIT_SSH_COMMAND=evil", "SECRET=token", "GIT_OPTIONAL_LOCKS=1",
	}
	environment := gitEnvironment(parent)
	for _, forbidden := range []string{"GIT_DIR=/elsewhere", "GIT_WORK_TREE=/w", "GIT_NAMESPACE=n", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_SSH_COMMAND=evil", "SECRET=token", "GIT_OPTIONAL_LOCKS=1"} {
		if slices.Contains(environment, forbidden) {
			t.Errorf("the Git environment passes through %s", forbidden)
		}
	}
	for _, required := range []string{
		"PATH=/bin", "HOME=/home/x", "TMPDIR=/tmp", "LANG=C", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
	} {
		if !slices.Contains(environment, required) {
			t.Errorf("the Git environment lacks %s: %v", required, environment)
		}
	}
	arguments := strings.Join(gitArguments("/repo", []string{"for-each-ref"}), " ")
	for _, required := range []string{"--no-optional-locks", "-C /repo", "protocol.allow=never", "protocol.ext.allow=never", "protocol.file.allow=never", "protocol.git.allow=never", "protocol.ssh.allow=never", "protocol.http.allow=never", "protocol.https.allow=never", "core.fsmonitor=false", "core.hooksPath=/dev/null", "uploadpack.allowFilter=false", "for-each-ref"} {
		if !strings.Contains(arguments, required) {
			t.Errorf("the Git arguments lack %s: %s", required, arguments)
		}
	}
}

// TestGitVersionFloorIsTwoFortyFive covers the version parser.
func TestGitVersionFloorIsTwoFortyFive(t *testing.T) {
	t.Parallel()
	for output, want := range map[string]bool{
		"git version 2.45.0":                 true,
		"git version 2.54.0 (Apple Git-157)": true,
		"git version 3.0.1":                  true,
		"git version 2.44.9":                 false,
		"git version 1.99.0":                 false,
		"git version 2":                      false,
		"git version x.y.z":                  false,
		"not git":                            false,
		"":                                   false,
	} {
		if got := gitVersionUsable(output); got != want {
			t.Errorf("gitVersionUsable(%q) = %v, want %v", output, got, want)
		}
	}
}

// TestDefaultBranchComesFromOriginsHEADThenTheCheckedOutBranch covers origin's
// symbolic HEAD, the checked-out branch and a detached HEAD with neither.
func TestDefaultBranchComesFromOriginsHEADThenTheCheckedOutBranch(t *testing.T) {
	t.Parallel()
	git := newFakeGit(t)
	dir, repo := indexedCheckout(t, git, 1)
	collectors := LocalCollectors{Runner: git}
	found := discover.Repo{Path: dir}
	repo.branch = "trunk"
	if got := collectors.DefaultBranch(t.Context(), found); got != "trunk" {
		t.Errorf("default branch from the checked-out branch = %q, want trunk", got)
	}
	repo.originHead = "main"
	if got := collectors.DefaultBranch(t.Context(), found); got != "main" {
		t.Errorf("default branch from origin's HEAD = %q, want main", got)
	}
	repo.originHead = ""
	repo.branch = ""
	if got := collectors.DefaultBranch(t.Context(), found); got != "" {
		t.Errorf("default branch of a detached clone with no origin HEAD = %q, want none", got)
	}
	git.reply(gitReply{Out: "elsewhere/main\n"})
	if got := collectors.DefaultBranch(t.Context(), found); got != "elsewhere/main" {
		t.Errorf("an origin HEAD that is not origin's is read as the checked-out branch: %q", got)
	}
}

// TestReadmeRefusesWhatGitReportsOddly drives the README read with a scripted
// Git that prints a malformed tree entry, an unparsable size, a size over the
// cap and a blob longer than its size said.
func TestReadmeRefusesWhatGitReportsOddly(t *testing.T) {
	t.Parallel()
	entry := "100644 blob " + readmeObject + "\tREADME.md\x00"
	for name, test := range map[string]struct {
		script func(*fakeGitRunner)
		want   error
	}{
		"a malformed tree entry": {func(g *fakeGitRunner) { g.when(commandIs("ls-tree"), gitReply{Out: "garbage\x00"}) }, errGit},
		"an entry with a bad id": {func(g *fakeGitRunner) { g.when(commandIs("ls-tree"), gitReply{Out: "100644 blob xyz\tREADME.md\x00"}) }, errGit},
		"a non-hex object id": {func(g *fakeGitRunner) {
			g.when(commandIs("ls-tree"), gitReply{Out: "100644 blob " + strings.Repeat("g", 40) + "\tREADME.md\x00"})
		}, errGit},
		"an unparsable size": {func(g *fakeGitRunner) {
			g.when(commandIs("ls-tree"), gitReply{Out: entry}).when(commandIs("cat-file", "-s"), gitReply{Out: "lots\n"})
		}, errGit},
		"a size over the cap": {func(g *fakeGitRunner) {
			g.when(commandIs("ls-tree"), gitReply{Out: entry}).when(commandIs("cat-file", "-s"), gitReply{Out: "2000000\n"})
		}, errReadmeTooLarge},
		"a blob over its size": {func(g *fakeGitRunner) {
			g.when(commandIs("ls-tree"), gitReply{Out: entry}).when(commandIs("cat-file", "-s"), gitReply{Out: "5\n"}).
				when(commandIs("cat-file", "blob"), gitReply{Out: strings.Repeat("a", MaxReadmeBytes+1)})
		}, errReadmeTooLarge},
		"a failing show-ref": {func(g *fakeGitRunner) { g.reply(gitReply{Exit: 128}) }, errGit},
		"a failing listing":  {func(g *fakeGitRunner) { g.when(commandIs("ls-tree"), gitReply{Exit: 128}) }, errGit},
		"a failing size": {func(g *fakeGitRunner) {
			g.when(commandIs("ls-tree"), gitReply{Out: entry}).when(commandIs("cat-file", "-s"), gitReply{Exit: 128})
		}, errGit},
		"a missing branch": {func(g *fakeGitRunner) { g.reply(gitReply{Exit: 1}) }, errReadmeAbsent},
		"no entry":         {func(g *fakeGitRunner) { g.when(commandIs("ls-tree"), gitReply{}) }, errReadmeAbsent},
	} {
		git := newFakeGit(t)
		dir, repo := indexedCheckout(t, git, 1)
		repo.readme["main"] = fakeReadme{Content: "x"}
		test.script(git)
		if _, err := (LocalCollectors{Runner: git}).Readme(t.Context(), discover.Repo{Path: dir}, "main"); !errors.Is(err, test.want) {
			t.Errorf("%s: %v, want %v", name, err, test.want)
		}
	}
}

// TestReadmeIsReadFromTheObjectStoreByItsObjectID follows the whole read: the
// branch ref, the tree entry, the size and the blob, each a read-only command
// naming the object, never a path in the working tree.
func TestReadmeIsReadFromTheObjectStoreByItsObjectID(t *testing.T) {
	t.Parallel()
	git := newFakeGit(t)
	dir, repo := indexedCheckout(t, git, 1)
	repo.readme["main"] = fakeReadme{Content: "# hello\n", Mode: "100755"}
	data, err := (LocalCollectors{Runner: git}).Readme(t.Context(), discover.Repo{Path: dir}, "main")
	if err != nil || string(data) != "# hello\n" {
		t.Fatalf("README = %q, %v", data, err)
	}
	var seen []string
	for _, call := range git.running() {
		seen = append(seen, call.String())
	}
	want := []string{
		"show-ref --verify --quiet refs/heads/main", "ls-tree -z refs/heads/main -- README.md",
		"cat-file -s " + readmeObject, "cat-file blob " + readmeObject,
	}
	if !slices.Equal(seen, want) {
		t.Errorf("commands = %q, want %q", seen, want)
	}
	if opts := git.running()[3].Opts; opts.StdoutLimit != MaxReadmeBytes {
		t.Errorf("the blob read is capped at %d, want %d", opts.StdoutLimit, MaxReadmeBytes)
	}
	for kind, mode := range map[string][2]string{"a symbolic link": {"120000", "blob"}, "a directory": {"040000", "tree"}, "a submodule": {"160000", "commit"}} {
		repo.readme["main"] = fakeReadme{Mode: mode[0], Kind: mode[1]}
		if _, err := (LocalCollectors{Runner: git}).Readme(t.Context(), discover.Repo{Path: dir}, "main"); !errors.Is(err, errReadmeNotRegular) {
			t.Errorf("%s: %v, want not regular", kind, err)
		}
	}
	if _, err := (LocalCollectors{Runner: git}).Readme(t.Context(), discover.Repo{Path: dir}, "no-such-branch"); !errors.Is(err, errReadmeAbsent) {
		t.Errorf("README of a missing branch = %v, want absent", err)
	}
}

// TestGitUsableAsksTheGitBinary covers a current Git, an old one and a failing
// one, over a scripted Git.
func TestGitUsableAsksTheGitBinary(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		reply gitReply
		want  bool
	}{
		"a current Git": {gitReply{Out: "git version 2.54.0\n"}, true},
		"an old Git":    {gitReply{Out: "git version 2.30.0\n"}, false},
		"a failing Git": {gitReply{Exit: 3}, false},
	} {
		if got := (LocalCollectors{Runner: newFakeGit(t).reply(test.reply)}).GitUsable(t.Context()); got != test.want {
			t.Errorf("%s: usable = %v, want %v", name, got, test.want)
		}
	}
}

// TestGitOutputNeverHoldsMoreThanItsCap requires output past the cap to fail
// with nothing returned, and a command that cannot start, or exits non-zero,
// to fail with a message that carries no text.
func TestGitOutputNeverHoldsMoreThanItsCap(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	big := newFakeGit(t).reply(gitReply{Out: strings.Repeat("a", 100)})
	if out, err := gitOutputLimited(ctx, big, "git", "/repo", 10, "x"); !errors.Is(err, errGitOutputTooLarge) || out != nil {
		t.Errorf("a command past its output cap = %q, %v", out, err)
	}
	if out, err := gitOutputLimited(ctx, big, "git", "/repo", 100, "x"); err != nil || len(out) != 100 {
		t.Errorf("a command at its output cap = %d bytes, %v", len(out), err)
	}
	for name, test := range map[string]struct {
		reply   gitReply
		missing bool
		code    int
	}{
		"a binary that is not there":  {gitReply{Err: &exec.Error{Name: "git", Err: exec.ErrNotFound}}, true, 0},
		"a path that does not exist":  {gitReply{Err: fs.ErrNotExist}, true, 0},
		"a binary that may not run":   {gitReply{Err: fs.ErrPermission}, true, 0},
		"a failure to start":          {gitReply{Err: errBoom}, false, 0},
		"a command that exits with 1": {gitReply{Exit: 1}, false, 1},
	} {
		_, err := readGit(ctx, newFakeGit(t).reply(test.reply), "git", "/repo", "x")
		var exit exitError
		switch {
		case !errors.Is(err, errGit) || err.Error() != errGit.Error():
			t.Errorf("%s: %v, want errGit with no text", name, err)
		case errors.Is(err, errCommandMissing) != test.missing:
			t.Errorf("%s: missing = %v, want %v", name, errors.Is(err, errCommandMissing), test.missing)
		case test.code != 0 && (!errors.As(err, &exit) || exit.code != test.code):
			t.Errorf("%s: %v, want exit status %d", name, err, test.code)
		}
	}
	if _, err := readGit(ctx, nil, filepath.Join(t.TempDir(), "no-such-git"), t.TempDir(), "x"); !errors.Is(err, errGit) {
		t.Errorf("the real runner on a Git binary that cannot start = %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := readGit(cancelled, newFakeGit(t), "git", "/repo", "x"); !errors.Is(err, errGit) {
		t.Errorf("a cancelled command = %v", err)
	}
	var exit error = exitError{code: 1}
	if !errors.Is(exit, errGit) || exit.Error() != errGit.Error() {
		t.Error("an exit error is not errGit")
	}
}
