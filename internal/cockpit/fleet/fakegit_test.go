package fleet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
	"gopkg.in/yaml.v3"
)

// The unit tier starts no process. Every Git command the collectors run goes
// through a runner.Runner, so these tests give them a scripted one: it answers
// the commands a Git repository would answer, from a small model of the
// repository, and records each call, with the exact argv (every hardening
// setting) and the environment the command was given. What real Git answers is
// proved against real repositories in the e2e tier (the *_e2e_test.go files).

// gitReply is what a scripted Git command answers: its standard output, a
// non-zero exit status, or an error that is not an exit (the command could not
// be started).
type gitReply struct {
	Out  string
	Exit int
	Err  error
}

// gitCall is one command the code under test ran.
type gitCall struct {
	Binary string
	// Dir is the repository named by -C, and Args the command's own arguments,
	// after the hardening settings every command carries.
	Dir  string
	Args []string
	Env  []string
	Opts runner.RunOptions
}

// String is the command's arguments, for matching and messages.
func (c gitCall) String() string { return strings.Join(c.Args, " ") }

// fakeGitRunner is a runner.Runner that answers Git commands. override, when
// set, answers first (and says whether it did); otherwise the repository model
// registered for the command's directory answers, and a directory with none is
// not a repository (Git's status 128).
type fakeGitRunner struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []gitCall
	repos    map[string]*fakeRepo
	override func(gitCall) (gitReply, bool)
}

var _ runner.Runner = (*fakeGitRunner)(nil)

func newFakeGit(t *testing.T) *fakeGitRunner {
	t.Helper()
	return &fakeGitRunner{t: t, repos: map[string]*fakeRepo{}}
}

// add registers the repository found at dir and returns it for the caller to
// shape.
func (f *fakeGitRunner) add(dir string, repo *fakeRepo) *fakeRepo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos[dir] = repo
	return repo
}

// reply makes every command answer with reply, whatever it is.
func (f *fakeGitRunner) reply(reply gitReply) *fakeGitRunner {
	f.override = func(gitCall) (gitReply, bool) { return reply, true }
	return f
}

// when makes the commands for which match is true answer with reply.
func (f *fakeGitRunner) when(match func(gitCall) bool, reply gitReply) *fakeGitRunner {
	previous := f.override
	f.override = func(c gitCall) (gitReply, bool) {
		if match(c) {
			return reply, true
		}
		if previous != nil {
			return previous(c)
		}
		return gitReply{}, false
	}
	return f
}

// running is the commands run so far.
func (f *fakeGitRunner) running() []gitCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// commandIs matches a command by its first arguments.
func commandIs(prefix ...string) func(gitCall) bool {
	return func(c gitCall) bool { return len(c.Args) >= len(prefix) && slices.Equal(c.Args[:len(prefix)], prefix) }
}

// commandMentions matches a command with text anywhere in its arguments.
func commandMentions(text string) func(gitCall) bool {
	return func(c gitCall) bool { return strings.Contains(c.String(), text) }
}

// pinRunOptions fails t unless a command was started the way every command of
// the snapshotter must be: no working directory of its own (Git is told its
// repository by -C), no standard input, standard error discarded, the wait
// delay, an output cap (zero would be uncapped), one output stream, and an
// environment that is set (a nil one would inherit the daemon's).
func pinRunOptions(t *testing.T, dir string, opts runner.RunOptions) {
	t.Helper()
	switch {
	case dir != "":
		t.Errorf("the command was given a working directory %q", dir)
	case len(opts.Stdin) != 0:
		t.Errorf("the command was given standard input")
	case !opts.DiscardStderr:
		t.Errorf("the command's standard error was not discarded")
	case opts.WaitDelay != gitWaitDelay:
		t.Errorf("the command's wait delay is %v, want %v", opts.WaitDelay, gitWaitDelay)
	case opts.StdoutLimit <= 0:
		t.Errorf("the command's output is not capped")
	case opts.CaptureCombined:
		t.Errorf("the command's output streams are combined")
	case opts.Env == nil:
		t.Errorf("the command's environment is nil, which inherits the daemon's")
	}
}

func (f *fakeGitRunner) RunOpts(ctx context.Context, workdir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	f.t.Helper()
	pinRunOptions(f.t, workdir, opts)
	if len(args) < 3 || args[1] != "-C" {
		f.t.Errorf("the command %s %v does not carry the hardening settings", name, args)
		return runner.Result{}, errBoom
	}
	dir := args[2]
	if want := gitArguments(dir, nil); !slices.Equal(args[:len(want)], want) {
		f.t.Errorf("the command %v does not begin with the hardening settings %v", args, want)
	}
	call := gitCall{Binary: name, Dir: dir, Args: slices.Clone(args[len(gitArguments(dir, nil)):]), Env: opts.Env, Opts: opts}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return runner.Result{}, err
	}
	reply, answered := gitReply{}, false
	if f.override != nil {
		reply, answered = f.override(call)
	}
	if !answered {
		f.mu.Lock()
		repo := f.repos[dir]
		f.mu.Unlock()
		reply = gitReply{Exit: 128}
		if repo != nil {
			reply = repo.answer(call.Args)
		}
	}
	return resultOf(reply, opts)
}

// resultOf is what the real runner returns for a command that answered reply.
func resultOf(reply gitReply, opts runner.RunOptions) (runner.Result, error) {
	switch {
	case reply.Err != nil:
		return runner.Result{}, reply.Err
	case reply.Exit != 0:
		return runner.Result{ExitCode: reply.Exit}, fmt.Errorf("exit status %d", reply.Exit)
	case opts.StdoutLimit > 0 && len(reply.Out) > opts.StdoutLimit:
		return runner.Result{Stdout: reply.Out[:opts.StdoutLimit]}, runner.ErrOutputTooLarge
	}
	return runner.Result{Stdout: reply.Out}, nil
}

func (f *fakeGitRunner) unexpected(op string) {
	f.t.Helper()
	f.t.Errorf("the code under test called %s, which only a Git command through RunOpts may", op)
}

func (f *fakeGitRunner) Run(context.Context, string, string, ...string) (runner.Result, error) {
	f.unexpected("Run")
	return runner.Result{}, errBoom
}

func (f *fakeGitRunner) RunWithInput(context.Context, string, []byte, string, ...string) (runner.Result, error) {
	f.unexpected("RunWithInput")
	return runner.Result{}, errBoom
}

func (f *fakeGitRunner) Start(context.Context, string, string, ...string) (runner.Handle, error) {
	f.unexpected("Start")
	return nil, errBoom
}

func (f *fakeGitRunner) Detach(string, string, ...string) (int, error) {
	f.unexpected("Detach")
	return 0, errBoom
}

func (f *fakeGitRunner) Interactive(context.Context, string, string, ...string) error {
	f.unexpected("Interactive")
	return errBoom
}

// fakeReadme is the README.md a branch's tip holds.
type fakeReadme struct {
	// Absent says the tree has no README.md.
	Absent bool
	// Mode and Kind are the tree entry's: 100644 blob by default.
	Mode, Kind string
	// Content is the blob's bytes; Size, when set, is what `cat-file -s`
	// reports instead of their length.
	Content string
	Size    string
}

// fakeRepo is the model of one repository the scripted Git answers from.
type fakeRepo struct {
	// head is the commit HEAD names ("" for an unborn branch), history the
	// commits reachable from it, oldest first and ending in head.
	head    string
	history []string
	// elsewhere are commits the repository holds that HEAD does not reach, and
	// trees the objects it holds that are not commits.
	elsewhere map[string]bool
	trees     map[string]bool
	shallow   bool
	// origin is remote.origin.url ("" when there is none), originHead the
	// branch origin's HEAD names ("" when it names none) and branch the checked
	// out branch ("" when detached).
	origin, originHead, branch string
	// refs is what for-each-ref prints.
	refs string
	// readme is the README.md at the tip of each branch that exists.
	readme map[string]fakeReadme
}

// newFakeRepo is a repository with commits (named c1, c2... in the 40-digit
// form of an object id) on branch main, and origin as its origin.
func newFakeRepo(commits int, origin string) *fakeRepo {
	repo := &fakeRepo{elsewhere: map[string]bool{}, trees: map[string]bool{}, origin: origin, branch: "main", readme: map[string]fakeReadme{}}
	for n := 1; n <= commits; n++ {
		repo.history = append(repo.history, objectID(n))
	}
	if commits > 0 {
		repo.head = repo.history[commits-1]
	}
	return repo
}

// objectID is the n-th full object id, 40 hexadecimal digits.
func objectID(n int) string { return fmt.Sprintf("%040x", n) }

// readmeObject is the object id of the README blob the model serves.
const readmeObject = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func (r *fakeRepo) position(sha string) int { return slices.Index(r.history, sha) }

func (r *fakeRepo) isCommit(sha string) bool { return r.position(sha) >= 0 || r.elsewhere[sha] }

// answer is what Git answers for args, the command's own arguments.
func (r *fakeRepo) answer(args []string) gitReply {
	switch args[0] {
	case "version":
		return gitReply{Out: "git version 2.54.0\n"}
	case "rev-parse":
		return r.revParse(args[1:])
	case "merge-base":
		// merge-base --is-ancestor <ancestor> <head>
		switch {
		case r.position(args[2]) >= 0:
			return gitReply{}
		case r.elsewhere[args[2]]:
			return gitReply{Exit: 1}
		}
		return gitReply{Exit: 128}
	case "rev-list":
		// rev-list --count <from>..<to>
		from, _, _ := strings.Cut(args[2], "..")
		if at := r.position(from); at >= 0 {
			return gitReply{Out: strconv.Itoa(len(r.history)-1-at) + "\n"}
		}
		return gitReply{Exit: 128}
	case "cat-file":
		return r.catFile(args[1:])
	case "symbolic-ref":
		return r.symbolicRef(args[1:])
	case "config":
		if r.origin == "" {
			return gitReply{Exit: 1}
		}
		return gitReply{Out: r.origin + "\n"}
	case "for-each-ref":
		return gitReply{Out: r.refs}
	case "show-ref":
		if _, found := r.readme[strings.TrimPrefix(args[len(args)-1], "refs/heads/")]; found {
			return gitReply{}
		}
		return gitReply{Exit: 1}
	case "ls-tree":
		return r.lsTree(args)
	}
	return gitReply{Exit: 129}
}

func (r *fakeRepo) revParse(args []string) gitReply {
	if args[0] == "--is-shallow-repository" {
		return gitReply{Out: strconv.FormatBool(r.shallow) + "\n"}
	}
	// rev-parse --verify --quiet HEAD
	if r.head == "" {
		return gitReply{Exit: 1}
	}
	return gitReply{Out: r.head + "\n"}
}

func (r *fakeRepo) catFile(args []string) gitReply {
	switch args[0] {
	case "-e":
		if commit, isCommit := strings.CutSuffix(args[1], "^{commit}"); isCommit {
			if r.isCommit(commit) {
				return gitReply{}
			}
			return gitReply{Exit: 128}
		}
		if r.isCommit(args[1]) || r.trees[args[1]] {
			return gitReply{}
		}
		return gitReply{Exit: 1}
	case "-s":
		return gitReply{Out: r.readmeSize() + "\n"}
	case "blob":
		return gitReply{Out: r.readmeContent()}
	}
	return gitReply{Exit: 129}
}

// readmeOfTip is the README of the only branch with one the model knows; the
// README tests use one branch at a time.
func (r *fakeRepo) readmeOfTip() fakeReadme {
	for _, entry := range r.readme {
		return entry
	}
	return fakeReadme{Absent: true}
}

func (r *fakeRepo) readmeSize() string {
	entry := r.readmeOfTip()
	if entry.Size != "" {
		return entry.Size
	}
	return strconv.Itoa(len(entry.Content))
}

func (r *fakeRepo) readmeContent() string { return r.readmeOfTip().Content }

func (r *fakeRepo) symbolicRef(args []string) gitReply {
	switch {
	case args[1] == "refs/remotes/origin/HEAD" && r.originHead != "":
		return gitReply{Out: "origin/" + r.originHead + "\n"}
	case args[1] == "HEAD" && r.branch != "":
		return gitReply{Out: r.branch + "\n"}
	}
	return gitReply{Exit: 128}
}

func (r *fakeRepo) lsTree(args []string) gitReply {
	entry, found := r.readme[strings.TrimPrefix(args[2], "refs/heads/")]
	if !found || entry.Absent {
		return gitReply{}
	}
	mode, kind := firstNonEmpty(entry.Mode, "100644"), firstNonEmpty(entry.Kind, "blob")
	return gitReply{Out: mode + " " + kind + " " + readmeObject + "\tREADME.md\x00"}
}

// writeManifestFile writes the creation record a WB worktree carries, straight
// into its journal directory: worktrees.WriteManifest also asks Git for the
// worktree's exclude file, which the unit tier does not run.
func writeManifestFile(t *testing.T, worktree string, manifest worktrees.Manifest) {
	t.Helper()
	directory := filepath.Join(worktree, ".wb", "local")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
