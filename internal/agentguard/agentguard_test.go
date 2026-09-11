package agentguard

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fixture is a projects root holding one canonical clone, one linked worktree
// under a separate WB worktrees root, one linked worktree nested inside the
// canonical clone, and one primary checkout outside the managed layout.
//
// It is built with real Git rather than hand-made directories: the whole guard
// turns on the difference between a `.git` directory and a `.git` file, and a
// fake of that difference would prove nothing about what Git actually writes.
type fixture struct {
	ProjectsRoot string
	Canonical    string
	Worktree     string
	Nested       string
	Foreign      string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "sneat-co", "backstage")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("create canonical clone: %v", err)
	}
	runGit(t, canonical, "init", "-q", "-b", "main")
	runGit(t, canonical, "config", "user.email", "guard@example.test")
	runGit(t, canonical, "config", "user.name", "guard")
	writeFile(t, filepath.Join(canonical, "README.md"), "canonical\n")
	runGit(t, canonical, "add", "-A")
	runGit(t, canonical, "commit", "-qm", "init")

	worktree := filepath.Join(root, "wbhome", "worktrees", "task", "sneat-co", "backstage")
	runGit(t, canonical, "worktree", "add", "-q", "-b", "task", worktree)

	nested := filepath.Join(canonical, ".claude", "worktrees", "nested")
	runGit(t, canonical, "worktree", "add", "-q", "-b", "nested", nested)

	foreign := filepath.Join(root, "scratch", "repo")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatalf("create foreign checkout: %v", err)
	}
	runGit(t, foreign, "init", "-q", "-b", "main")

	return fixture{
		ProjectsRoot: projectsRoot,
		Canonical:    canonical,
		Worktree:     worktree,
		Nested:       nested,
		Foreign:      foreign,
	}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(arguments, " "), directory, err, output)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestClassifyDistinguishesCanonicalFromLinked pins the single question the
// whole guard rests on. A linked worktree that read as canonical would refuse
// every agent's real work; a canonical clone that read as linked would leave
// the clone unprotected.
func TestClassifyDistinguishesCanonicalFromLinked(t *testing.T) {
	repositories := newFixture(t)
	cases := []struct {
		name string
		path string
		want Kind
	}{
		{"canonical clone root", repositories.Canonical, KindCanonical},
		{"file inside a canonical clone", filepath.Join(repositories.Canonical, "spec", "lessons", "x.md"), KindCanonical},
		{"managed worktree", repositories.Worktree, KindLinked},
		{"file inside a managed worktree", filepath.Join(repositories.Worktree, "internal", "x.go"), KindLinked},
		{"worktree nested inside a canonical clone", repositories.Nested, KindLinked},
		{"file inside a nested worktree", filepath.Join(repositories.Nested, "cmd", "x.go"), KindLinked},
		{"primary checkout outside the managed layout", repositories.Foreign, KindForeign},
		{"the projects root itself", repositories.ProjectsRoot, KindUnknown},
		{"the owner directory", filepath.Dir(repositories.Canonical), KindUnknown},
		{"a path in no repository", filepath.Join(t.TempDir(), "note.txt"), KindUnknown},
		{"a relative path", "spec/lessons/x.md", KindUnknown},
		{"an empty path", "", KindUnknown},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := Classify(repositories.ProjectsRoot, testCase.path)
			if got.Kind != testCase.want {
				t.Fatalf("Classify(%s) = %q, want %q", testCase.path, got.Kind, testCase.want)
			}
			if testCase.want == KindCanonical && got.Slug() != "sneat-co/backstage" {
				t.Fatalf("canonical slug = %q, want sneat-co/backstage", got.Slug())
			}
		})
	}
}

// TestClassifyProtectsADotPrefixedRepository covers <owner>/.github, which
// every organisation has and WB clones like any other repository. Rejecting it
// as an internal directory left thirteen canonical clones on the real fleet
// unguarded, while `<projects-root>/.wb` must still never read as a coordinate.
func TestClassifyProtectsADotPrefixedRepository(t *testing.T) {
	repositories := newFixture(t)
	profile := filepath.Join(repositories.ProjectsRoot, "sneat-co", ".github")
	if err := os.MkdirAll(filepath.Join(profile, ".git"), 0o755); err != nil {
		t.Fatalf("create the profile repository: %v", err)
	}
	location := Classify(repositories.ProjectsRoot, filepath.Join(profile, "workflows", "ci.yml"))
	if location.Kind != KindCanonical {
		t.Fatalf("Classify(<owner>/.github) = %q, want %q", location.Kind, KindCanonical)
	}
	if location.Slug() != "sneat-co/.github" {
		t.Fatalf("slug = %q, want sneat-co/.github", location.Slug())
	}
	decision := Inspect(bashCall("git checkout -- .", profile), Options{ProjectsRoot: repositories.ProjectsRoot})
	if !decision.Deny {
		t.Fatal("a write into <owner>/.github was allowed")
	}

	// WB's own hierarchy is still not a repository coordinate.
	internal := filepath.Join(repositories.ProjectsRoot, ".wb", "worktrees")
	if err := os.MkdirAll(filepath.Join(internal, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Classify(repositories.ProjectsRoot, internal); got.Kind == KindCanonical {
		t.Fatalf("<projects-root>/.wb/worktrees read as a canonical clone: %+v", got)
	}
}

// TestClassifyFollowsASymlinkedGitDirectory covers a canonical clone whose
// .git is a symlink to a directory. Reading that as a file would silently
// downgrade the clone to a writable worktree.
func TestClassifyFollowsASymlinkedGitDirectory(t *testing.T) {
	repositories := newFixture(t)
	relocated := filepath.Join(t.TempDir(), "backstage.git")
	if err := os.Rename(filepath.Join(repositories.Canonical, ".git"), relocated); err != nil {
		t.Fatalf("relocate the git directory: %v", err)
	}
	if err := os.Symlink(relocated, filepath.Join(repositories.Canonical, ".git")); err != nil {
		t.Fatalf("symlink the git directory: %v", err)
	}
	if got := Classify(repositories.ProjectsRoot, repositories.Canonical); got.Kind != KindCanonical {
		t.Fatalf("Classify with a symlinked .git = %q, want %q", got.Kind, KindCanonical)
	}
}

// TestClassifyAcceptsASymlinkedProjectsRoot covers the macOS case where a
// projects root reaches the guard through /tmp while paths arrive resolved
// through /private/tmp, or the reverse.
func TestClassifyAcceptsASymlinkedProjectsRoot(t *testing.T) {
	repositories := newFixture(t)
	link := filepath.Join(t.TempDir(), "projects-link")
	if err := os.Symlink(repositories.ProjectsRoot, link); err != nil {
		t.Fatalf("symlink the projects root: %v", err)
	}
	if got := Classify(link, repositories.Canonical); got.Kind != KindCanonical {
		t.Fatalf("Classify through a symlinked projects root = %q, want %q", got.Kind, KindCanonical)
	}
}

func bashCall(command, cwd string) ToolCall {
	return ToolCall{
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
		CWD:           cwd,
		ToolInput:     json.RawMessage(`{"command":` + mustJSON(command) + `}`),
	}
}

func mustJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// TestBashRefusesWritesIntoACanonicalClone covers the constructs actually seen
// in the violations this guard was built for, plus the managed-hook bypass
// that made a pre-commit hook insufficient on its own.
func TestBashRefusesWritesIntoACanonicalClone(t *testing.T) {
	repositories := newFixture(t)
	commands := []struct {
		name    string
		command string
		cwd     string
	}{
		{"pathspec checkout, the 186-file violation", "git checkout origin/main -- .", repositories.Canonical},
		{"pathspec checkout with an explicit path", "git checkout -- spec/lessons", repositories.Canonical},
		{"checkout of the whole tree", "git checkout .", repositories.Canonical},
		{"branch creation", "git checkout -b feature/x", repositories.Canonical},
		{"reset", "git reset --hard origin/main", repositories.Canonical},
		{"plain reset", "git reset", repositories.Canonical},
		{"restore", "git restore spec/", repositories.Canonical},
		{"apply", "git apply /tmp/patch.diff", repositories.Canonical},
		{"stash", "git stash", repositories.Canonical},
		{"stash push", "git stash push -m wip", repositories.Canonical},
		{"clean", "git clean -fd", repositories.Canonical},
		{"add", "git add -A", repositories.Canonical},
		{"commit", "git commit -m x", repositories.Canonical},
		{"rebase", "git rebase origin/main", repositories.Canonical},
		{"cherry-pick", "git cherry-pick abc123", repositories.Canonical},
		{"non-fast-forward merge", "git merge feature/x", repositories.Canonical},
		{"pull without --ff-only", "git pull", repositories.Canonical},
		{"git -C reaching into a clone from elsewhere", "git -C " + repositories.Canonical + " reset --hard", repositories.Worktree},
		{"cd then write", "cd " + repositories.Canonical + " && git add -A", repositories.Worktree},
		{"redirection into a clone", "echo hi > " + filepath.Join(repositories.Canonical, "note.md"), repositories.Worktree},
		{"heredoc into a clone", "cat > " + filepath.Join(repositories.Canonical, "note.md") + " <<'EOF'\nbody\nEOF", repositories.Worktree},
		{"appending redirection", "echo hi >> " + filepath.Join(repositories.Canonical, "note.md"), repositories.Worktree},
		{"sed in place", "sed -i '' s/a/b/ " + filepath.Join(repositories.Canonical, "README.md"), repositories.Worktree},
		{"sed in place with a suffix", "sed -i.bak s/a/b/ README.md", repositories.Canonical},
		{"rm inside a clone", "rm -rf " + filepath.Join(repositories.Canonical, "spec"), repositories.Worktree},
		{"mv into a clone", "mv /tmp/x " + filepath.Join(repositories.Canonical, "x"), repositories.Worktree},
		{"specscore generating in the clone, the second violation", "specscore lesson new some-gap", repositories.Canonical},
		{"specscore change-status in the clone", "specscore feature change-status x --to Approved", repositories.Canonical},
		{"go mod tidy in the clone", "go mod tidy", repositories.Canonical},
		{"pnpm install in the clone", "pnpm install", repositories.Canonical},
		{"gofmt rewriting the clone", "gofmt -w ./...", repositories.Canonical},
		{"a write after a read in the same line", "git status && git reset --hard", repositories.Canonical},
		{"a write behind a pipeline", "true | git add -A", repositories.Canonical},
	}
	for _, testCase := range commands {
		t.Run(testCase.name, func(t *testing.T) {
			decision := Inspect(bashCall(testCase.command, testCase.cwd), Options{ProjectsRoot: repositories.ProjectsRoot})
			if !decision.Deny {
				t.Fatalf("Inspect(%q) allowed the call; want deny", testCase.command)
			}
			for _, expected := range []string{"canonical clone", "wb worktree create", "sneat-co/backstage"} {
				if !strings.Contains(decision.Reason, expected) {
					t.Fatalf("refusal for %q does not name %q:\n%s", testCase.command, expected, decision.Reason)
				}
			}
		})
	}
}

// TestBashAllowsWhatACanonicalCloneExistsToDo is the half of the contract that
// protects the fleet from the guard. Fetching and fast-forwarding is a
// canonical clone's entire job, and every read of one is legitimate.
func TestBashAllowsWhatACanonicalCloneExistsToDo(t *testing.T) {
	repositories := newFixture(t)
	commands := []struct {
		name    string
		command string
		cwd     string
	}{
		{"fetch", "git fetch --all --prune", repositories.Canonical},
		{"fast-forward merge", "git merge --ff-only origin/main", repositories.Canonical},
		{"fetch then fast-forward", "git fetch && git merge --ff-only origin/main", repositories.Canonical},
		{"fast-forward pull", "git pull --ff-only", repositories.Canonical},
		{"status", "git status --porcelain", repositories.Canonical},
		{"log", "git log --oneline -20", repositories.Canonical},
		{"show", "git show HEAD:README.md", repositories.Canonical},
		{"ls-tree", "git ls-tree -r --name-only HEAD", repositories.Canonical},
		{"diff", "git diff origin/main", repositories.Canonical},
		{"rev-parse", "git rev-parse HEAD", repositories.Canonical},
		{"branch listing", "git branch -a --contains abc", repositories.Canonical},
		{"worktree listing", "git worktree list", repositories.Canonical},
		{"push", "git push origin main", repositories.Canonical},
		{"apply --check", "git apply --check /tmp/x.diff", repositories.Canonical},
		{"clean --dry-run", "git clean -nd", repositories.Canonical},
		{"clean -n", "git clean -n", repositories.Canonical},
		{"clean --dry-run spelled out", "git clean --dry-run -d", repositories.Canonical},
		{"stash list", "git stash list", repositories.Canonical},
		{"merge --abort", "git merge --abort", repositories.Canonical},
		{"bare branch switch back to base", "git checkout main", repositories.Canonical},
		{"grep", "grep -rn TODO .", repositories.Canonical},
		{"read redirected to a file elsewhere", "git log > /tmp/log.txt", repositories.Canonical},
		{"redirect to /dev/null", "git status > /dev/null 2>&1", repositories.Canonical},
		{"sed without -i", "sed s/a/b/ README.md", repositories.Canonical},
		{"specscore read verb", "specscore lesson list --not-enforced", repositories.Canonical},
		{"go build", "go build ./...", repositories.Canonical},
		{"go test", "go test ./...", repositories.Canonical},
		{"wb itself, the remedy the refusal names", "wb worktree create task sneat-co/backstage", repositories.Canonical},
		{"wb guard", "wb worktree guard .", repositories.Canonical},
		{"every write, inside a managed worktree", "git add -A && git commit -m x && git reset --hard", repositories.Worktree},
		{"every write, inside a nested worktree", "git checkout -- . && rm -rf spec", repositories.Nested},
		{"every write, inside a foreign checkout", "git reset --hard && rm -rf .", repositories.Foreign},
		{"a write in an unrelated directory", "rm -rf /tmp/scratch", "/tmp"},
		{"a working directory reached through a variable", `cd "$REPO" && git reset --hard`, repositories.Canonical},
		{"a heredoc body that looks like shell", "cat <<'EOF' | wc -l\ngit reset --hard\nEOF", repositories.Canonical},
	}
	for _, testCase := range commands {
		t.Run(testCase.name, func(t *testing.T) {
			decision := Inspect(bashCall(testCase.command, testCase.cwd), Options{ProjectsRoot: repositories.ProjectsRoot})
			if decision.Deny {
				t.Fatalf("Inspect(%q) refused a legitimate call:\n%s", testCase.command, decision.Reason)
			}
		})
	}
}

// TestBashAllowsHelpInvocationsOfGuardedTools pins wb#493: a read-only
// `--help`/`-h`/`help` invocation of a tool this guard otherwise judges by
// write verb was refused exactly like the write it was only asking about,
// because the verb scan matches anywhere in the argument list. Every command
// here is recognised as help by its own tool's shape (see requestsHelp):
// cobra-based specscore keeps a subcommand chain (or none) trailing in one
// --help/-h, or a bare `help` subcommand, but go/npm/pnpm/yarn/bun only
// recognise the bare `<tool> --help`/`<tool> -h` invocation or `<tool> help
// ...` — a subcommand in front of --help/-h is NOT safe on those five
// (wb#500 third review) and is inspected normally instead of bypassed (see
// TestBashSubcommandBeforeHelpIsInspectedNormallyForPassThroughTools and
// TestBashCommandsWithAFlagBesideHelpAreInspectedNormallyNotBypassed for the
// shapes that no longer qualify, or never did).
func TestBashAllowsHelpInvocationsOfGuardedTools(t *testing.T) {
	repositories := newFixture(t)
	commands := []string{
		"specscore feature change-status --help",
		"specscore feature change-status x -h",
		"specscore help feature change-status",
		"specscore --help",
		"go --help",
		"go -h",
		"go help mod tidy",
		"go help build",
		"pnpm --help",
		"yarn --help",
		"bun --help",
		"npm help install",
		"npm help run",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			if decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot}); decision.Deny {
				t.Fatalf("Inspect(%q) refused a read-only --help invocation:\n%s", command, decision.Reason)
			}
		})
	}
}

// TestBashCommandsWithAFlagBesideHelpAreInspectedNormallyNotBypassed pins the
// wb#500 second review's Should-fix 1 head-on: the pre-fix requestsHelp
// treated --help/-h as terminal no matter where else on the line it sat, so
// a value-taking flag positioned just before it could swallow it as that
// flag's own value, and a `--` separator could hand it to a script instead
// of the wrapper — in both cases the tool never saw a help request at all,
// yet the whole line was waved through as if it had. Reproduced against the
// real binaries before this fix (see the review): `specscore feature
// change-status <id> --caller --help --to Approved` gets past flag parsing
// and only fails on project lookup, proving --caller consumed --help as its
// value; `npm run build -- --help` and `pnpm run build -- --help` actually
// run the build script, proving -- handed --help to the script. Now that
// requestsHelp only recognises a bare shape, none of these are treated as
// help: the specscore line is inspected normally and refused as a
// change-status write inside a canonical clone, and the npm/pnpm lines still
// hit the governed-validation gate inside a managed worktree — see
// TestBashSpecscoreCallerFlagBeforeHelpIsRefusedAsAWrite and
// TestBashPackageManagerRunScriptDashDashHelpStillGovernedValidation for the
// full repro.
func TestBashCommandsWithAFlagBesideHelpAreInspectedNormallyNotBypassed(t *testing.T) {
	repositories := newFixture(t)
	// Every one of these has --help/-h as the final word but at least one
	// other flag earlier on the line, so requestsHelp must not recognise any
	// of them as a bare help request — they fall through to normal
	// inspection, which is a write refusal for the ones that name a write
	// verb with the clone as the working directory.
	commands := []string{
		"specscore feature change-status x --to Approved --help",
		"specscore feature change-status x --to Approved -h",
		"go mod tidy -x --help",
		"pnpm install --no-optional --help",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
			if !decision.Deny {
				t.Fatalf("Inspect(%q) treated a flag-plus-help line as a bare help request; want it inspected (and refused) normally", command)
			}
		})
	}
}

// TestBashStillRefusesWritesNamedAlongsideHelpText covers the write side of
// wb#493: --help is only ever a terminal, non-mutating request, so appending
// it to an unrelated write must never suppress that write's refusal, and a
// bare "help" appearing as an ORDINARY ARGUMENT (not the subcommand position)
// must not be mistaken for the help subcommand either.
func TestBashStillRefusesWritesNamedAlongsideHelpText(t *testing.T) {
	repositories := newFixture(t)
	commands := []string{
		"specscore feature change-status x --to Approved",
		"specscore lesson new help",
		"go mod tidy",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			if decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot}); !decision.Deny {
				t.Fatalf("Inspect(%q) allowed a write; want deny", command)
			}
		})
	}
}

// TestHooksAreNeverBypassedInAnyManagedWorktree pins
// lesson:work-preservation-is-never-grounds-to-bypass-a-hook /
// rule:hooks-are-never-bypassed: a hook bypass has no legitimate reading
// anywhere WB manages, not only a canonical clone (the original scope of
// internal/agentguard/git.go before this policy), and the refusal message is
// distinct from the canonical-clone wording.
func TestHooksAreNeverBypassedInAnyManagedWorktree(t *testing.T) {
	repositories := newFixture(t)
	commands := []struct {
		name    string
		command string
		cwd     string
	}{
		{"hooks bypass, the commit that landed anyway", `git -c core.hooksPath=/dev/null commit -q -m x`, repositories.Canonical},
		{"hooks bypass by any casing", `git -c CORE.HOOKSPATH=/dev/null commit -m x`, repositories.Canonical},
		{"hooks bypass in a linked worktree, not only the canonical clone", `git -c core.hooksPath=/dev/null commit -m x`, repositories.Worktree},
		{"hooks bypass in a nested worktree", `git -c core.hooksPath=/dev/null commit -m x`, repositories.Nested},
		{"no-verify bypass on commit", "git commit --no-verify -m x", repositories.Canonical},
		{"no-verify bypass on push", "git push --no-verify origin main", repositories.Canonical},
		{"no-verify bypass on push, in a worktree", "git push --no-verify origin main", repositories.Worktree},
		{"no-verify bypass on merge", "git merge --no-verify feature/x", repositories.Canonical},
		{"-n short flag on commit means --no-verify", "git commit -n -m x", repositories.Canonical},
		{"git config sets core.hooksPath", "git config core.hooksPath /dev/null", repositories.Worktree},
		{"git config reads core.hooksPath", "git config --get core.hooksPath", repositories.Worktree},
		{"git config unsets core.hooksPath", "git config --unset core.hooksPath", repositories.Canonical},
	}
	for _, testCase := range commands {
		t.Run(testCase.name, func(t *testing.T) {
			decision := Inspect(bashCall(testCase.command, testCase.cwd), Options{ProjectsRoot: repositories.ProjectsRoot})
			if !decision.Deny {
				t.Fatalf("Inspect(%q) allowed a hook bypass", testCase.command)
			}
			for _, expected := range []string{"hooks-are-never-bypassed", "managed hooks", "wb worktree rescue --push"} {
				if !strings.Contains(decision.Reason, expected) {
					t.Fatalf("refusal for %q does not name %q:\n%s", testCase.command, expected, decision.Reason)
				}
			}
			if strings.Contains(decision.Reason, "must stay clean") {
				t.Fatalf("hook-bypass refusal for %q used the canonical-clone wording:\n%s", testCase.command, decision.Reason)
			}
		})
	}
}

// TestHookBypassFalsePositives pins the constructs that look similar to a
// hook bypass but are not one, per the same short-flag ambiguity the git.go
// bypassesManagedHooks doc comment explains: `-n` means something else on
// push and merge than it does on commit.
func TestHookBypassFalsePositives(t *testing.T) {
	repositories := newFixture(t)
	commands := []struct {
		name    string
		command string
		cwd     string
	}{
		{"push -n is --dry-run, not --no-verify", "git push -n origin main", repositories.Canonical},
		{"merge -n is --no-stat, not --no-verify", "git merge -n --ff-only origin/main", repositories.Canonical},
		{"an unrelated git config key", "git config user.email agent@example.test", repositories.Canonical},
		{"a config value that merely mentions hooksPath in prose", "git config commit.template hooksPath-notes.txt", repositories.Canonical},
		{"hooks bypass outside any managed checkout", "git -c core.hooksPath=/dev/null commit -m x", "/tmp/scratch"},
	}
	for _, testCase := range commands {
		t.Run(testCase.name, func(t *testing.T) {
			decision := Inspect(bashCall(testCase.command, testCase.cwd), Options{ProjectsRoot: repositories.ProjectsRoot})
			if decision.Deny {
				t.Fatalf("Inspect(%q) refused a legitimate call:\n%s", testCase.command, decision.Reason)
			}
		})
	}
}

// TestBashRefusesGhPrMerge pins rule:land-with-wb-verb (sneat-co/backstage):
// three merger lanes reimplemented landing step by step with `gh pr merge`
// instead of the `wb worktree land` / `wb pr land` verbs their own contract
// already named. The refusal fires regardless of chaining, subshells, and
// working directory — none of that is what made those lanes go around WB.
func TestBashRefusesGhPrMerge(t *testing.T) {
	repositories := newFixture(t)
	commands := []struct {
		name    string
		command string
		cwd     string
	}{
		{"plain merge", "gh pr merge", repositories.Canonical},
		{"merge by number", "gh pr merge 1041", repositories.Worktree},
		{"merge with flags", "gh pr merge 1041 --squash --admin", "/tmp"},
		{"a global flag before pr", "gh --repo sneat-co/sneat-go pr merge 1041", repositories.Worktree},
		{"chained after a read", "gh pr checks 1041 && gh pr merge 1041", repositories.Worktree},
		{"chained with ;", "gh pr view 1041; gh pr merge 1041", repositories.Worktree},
		{"inside a subshell", "(gh pr merge 1041)", repositories.Worktree},
		{"behind a pipeline", "true | gh pr merge 1041", repositories.Worktree},
		// wb#500 final review, S1: gh picks the subcommand with cobra's
		// lookup, so a flag between pr and merge, or a flag the lookup reads
		// as taking the next word, still reaches the merge.
		{"-R between pr and merge", "gh pr -R sneat-co/sneat-go merge 1041", repositories.Worktree},
		{"--repo between pr and merge, flags after", "gh pr --repo sneat-co/sneat-go merge 1041 --squash", repositories.Worktree},
		{"an attached -R value between pr and merge", "gh pr -Rsneat-co/sneat-go merge 1041", repositories.Worktree},
		{"--repo=value between pr and merge", "gh pr --repo=sneat-co/sneat-go merge 1041", repositories.Worktree},
		{"-R before pr", "gh -R sneat-co/sneat-go pr merge 1041", repositories.Worktree},
		{"a merge flag before pr that the lookup reads as taking the number", "gh --squash 1041 pr merge", repositories.Worktree},
		{"a merge flag between pr and merge that the lookup reads as taking the number", "gh pr --squash 1041 merge", repositories.Worktree},
		{"a short merge flag between pr and merge", "gh pr -s 1041 merge", repositories.Worktree},
		{"an =value flag between pr and merge", "gh pr --admin=true merge 1041", repositories.Worktree},
		// wb#500 final review, S3: a shell keyword opens a command in the
		// same segment, so a loop or conditional body is inspected.
		{"a for loop body", `for n in 12 13; do gh pr merge "$n" --squash; done`, repositories.Worktree},
		{"a multi-line for loop body", "for n in 12 13\ndo\n  gh pr merge \"$n\"\ndone", repositories.Worktree},
		{"a while-read loop body", `while read n; do gh pr merge "$n"; done < prs.txt`, repositories.Worktree},
		{"an if condition", "if gh pr merge 1041; then echo merged; fi", repositories.Worktree},
		{"a then branch", "if true; then gh pr merge 1041; fi", repositories.Worktree},
		{"an else branch", "if false; then :; else gh pr merge 1041; fi", repositories.Worktree},
		{"an elif condition", "if false; then :; elif gh pr merge 1041; then :; fi", repositories.Worktree},
		{"an until condition", "until gh pr merge 1041; do sleep 5; done", repositories.Worktree},
		{"negated with !", "! gh pr merge 1041", repositories.Worktree},
		{"stacked keywords", "if ! gh pr merge 1041; then echo failed; fi", repositories.Worktree},
		{"coproc", "coproc gh pr merge 1041", repositories.Worktree},
		// S3: a wrapper is seen through only once its own options and their
		// values are skipped too.
		{"nice -n", "nice -n 10 gh pr merge 1041", repositories.Worktree},
		{"nice -N", "nice -10 gh pr merge 1041", repositories.Worktree},
		{"nice --adjustment value", "nice --adjustment 5 gh pr merge 1041", repositories.Worktree},
		{"sudo -u", "sudo -u alex gh pr merge 1041", repositories.Worktree},
		{"sudo with several options", "sudo -E -u alex -g staff gh pr merge 1041", repositories.Worktree},
		{"sudo --user value", "sudo --user alex gh pr merge 1041", repositories.Worktree},
		{"sudo --user=value", "sudo --user=alex gh pr merge 1041", repositories.Worktree},
		{"sudo clustered -iu value", "sudo -iu alex gh pr merge 1041", repositories.Worktree},
		{"time -p", "time -p gh pr merge 1041", repositories.Worktree},
		{"/usr/bin/time -o file", "/usr/bin/time -o /tmp/timing gh pr merge 1041", repositories.Worktree},
		{"stdbuf -oL", "stdbuf -oL gh pr merge 1041", repositories.Worktree},
		{"stdbuf -o L", "stdbuf -o L gh pr merge 1041", repositories.Worktree},
		{"exec -a", "exec -a name gh pr merge 1041", repositories.Worktree},
		{"exec -la", "exec -la name gh pr merge 1041", repositories.Worktree},
		{"command -p", "command -p gh pr merge 1041", repositories.Worktree},
		{"builtin command", "builtin command gh pr merge 1041", repositories.Worktree},
		{"zsh noglob", "noglob gh pr merge 1041", repositories.Worktree},
		{"nohup", "nohup gh pr merge 1041", repositories.Worktree},
		{"timeout DURATION", "timeout 60 gh pr merge 1041", repositories.Worktree},
		{"timeout options and DURATION", "timeout -s KILL -k 5 60 gh pr merge 1041", repositories.Worktree},
		{"timeout --signal=value", "timeout --signal=KILL 60 gh pr merge 1041", repositories.Worktree},
		{"caffeinate -i", "caffeinate -i gh pr merge 1041", repositories.Worktree},
		{"caffeinate -t value", "caffeinate -t 5 gh pr merge 1041", repositories.Worktree},
		{"xargs", "echo 1041 | xargs gh pr merge", repositories.Worktree},
		{"xargs -n 1", "echo 12 13 | xargs -n 1 gh pr merge --squash", repositories.Worktree},
		{"xargs -n1", "echo 12 13 | xargs -n1 gh pr merge", repositories.Worktree},
		{"xargs -I {}", "echo 1041 | xargs -I {} gh pr merge {}", repositories.Worktree},
		{"xargs -I{}", "echo 1041 | xargs -I{} gh pr merge {}", repositories.Worktree},
		{"env -u value", "env -u GH_TOKEN gh pr merge 1041", repositories.Worktree},
		{"env -i", "env -i gh pr merge 1041", repositories.Worktree},
		{"env -P value", "env -P /usr/bin gh pr merge 1041", repositories.Worktree},
		{"an assignment before a wrapper", "FOO=bar nice gh pr merge 1041", repositories.Worktree},
		{"wb run --", "wb run -- gh pr merge 1041", repositories.Worktree},
		{"wb run with its own flags", "wb run --quiet -- gh pr merge 1041", repositories.Worktree},
		{"a wb root flag before run", "wb --projects-root /tmp/projects run -- gh pr merge 1041", repositories.Worktree},
		{"stacked wrappers with options", "sudo -u alex nice -n 5 timeout 60 gh pr merge 1041", repositories.Worktree},
		{"an absolute wrapper path", "/usr/bin/env gh pr merge 1041", repositories.Worktree},
	}
	for _, testCase := range commands {
		t.Run(testCase.name, func(t *testing.T) {
			decision := Inspect(bashCall(testCase.command, testCase.cwd), Options{ProjectsRoot: repositories.ProjectsRoot})
			if !decision.Deny {
				t.Fatalf("Inspect(%q) allowed the call; want deny", testCase.command)
			}
			for _, expected := range []string{"land-with-wb-verb", "wb worktree land", "wb pr land", ghPrMergeOverrideEnv} {
				if !strings.Contains(decision.Reason, expected) {
					t.Fatalf("refusal for %q does not name %q:\n%s", testCase.command, expected, decision.Reason)
				}
			}
		})
	}
}

// TestBashAllowsGhReadsAndTheWBLandingVerbs is the other half of the
// contract: `gh pr merge` is the only refused shape, `wb` itself (the remedy
// the refusal names) is always allowed, and every other `gh pr` subcommand
// stays read-only from this guard's perspective.
func TestBashAllowsGhReadsAndTheWBLandingVerbs(t *testing.T) {
	repositories := newFixture(t)
	commands := []string{
		"gh pr view 1041",
		"gh pr checks 1041",
		"gh pr list",
		"gh pr status",
		"gh pr diff 1041",
		"gh repo view",
		"wb worktree land .",
		"wb pr land sneat-co/sneat-go#1041",
		// wb#500 final review, S1: only a call gh resolves to pr merge is
		// refused. Against gh 2.100.0 these print help, search, or fail with
		// unknown command.
		"gh help pr merge",
		"gh search issues pr merge --repo sneat-co/sneat-go",
		"gh search prs merge",
		"gh pr list --search merge",
		"gh -h pr merge 1041",
		"gh -- pr merge 1041",
		"gh pr -- merge 1041",
		"wb land .",
		"wb run -- gh pr view 1041",
		`for n in 12 13; do gh pr view "$n"; done`,
		"if gh pr checks 1041; then wb pr land sneat-co/sneat-go#1041; fi",
		"echo 1041 | xargs gh pr view",
		"timeout 60 gh pr checks 1041 --watch",
		"sudo -u alex gh pr view 1041",
		"command -v gh",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			if decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot}); decision.Deny {
				t.Fatalf("Inspect(%q) refused a legitimate call:\n%s", command, decision.Reason)
			}
		})
	}
}

// TestBashGhPrMergeTextIsNotACall pins the no-false-positive half of the
// land-with-wb-verb policy. The words "gh pr merge" in a commit message, a
// heredoc body, a search pattern or an echo are text, not a call, and the
// wider keyword and wrapper walk (wb#500 final review, S3) must not start
// reading them as one.
func TestBashGhPrMergeTextIsNotACall(t *testing.T) {
	repositories := newFixture(t)
	commands := []string{
		`git commit -m "docs: replace gh pr merge 12 with wb land"`,
		"git commit -F - <<'EOF'\nland: stop running gh pr merge 12 by hand\nEOF",
		"cat > notes.md <<'EOF'\nfor n in 12 13; do gh pr merge \"$n\"; done\nEOF",
		`grep -rn "gh pr merge" docs/`,
		`rg 'gh pr merge' --type md`,
		`echo "never run gh pr merge 12"`,
		`printf '%s\n' "gh pr merge" | xargs echo`,
		`if grep -q "gh pr merge" notes.md; then echo found; fi`,
		`bash -c 'echo gh pr merge 12'`,
		"gh pr merge --help",
		"wb pr land sneat-co/sneat-go#12",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			if decision := Inspect(bashCall(command, repositories.Worktree), Options{ProjectsRoot: repositories.ProjectsRoot}); decision.Deny {
				t.Fatalf("Inspect(%q) refused text that only mentions gh pr merge:\n%s", command, decision.Reason)
			}
		})
	}
}

// TestGhPrMergeHelpAloneIsStillAllowed pins the false-positive half of the
// wb#500 review's Blocker 1: a genuine `gh pr merge --help`/`-h` — nothing
// else on the line that could consume it as a value — prints help and merges
// nothing, so it must stay allowed exactly as it was before the fix.
func TestGhPrMergeHelpAloneIsStillAllowed(t *testing.T) {
	repositories := newFixture(t)
	commands := []string{
		"gh pr merge --help",
		"gh pr merge -h",
		"gh pr merge 1041 --help",
		"gh pr merge 1041 --squash --help",
		"gh --repo sneat-co/sneat-go pr merge --help",
		"gh help pr merge",
		"gh --help pr merge 1041",
		"gh pr --help merge 1041",
		"gh pr -R sneat-co/sneat-go merge --help",
		"gh pr merge 1041 -sh",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			if decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot}); decision.Deny {
				t.Fatalf("Inspect(%q) refused a genuine --help request:\n%s", command, decision.Reason)
			}
		})
	}
}

// TestBashHelpTokenNeverBypassesAnUnrelatedGuard pins the wb#500 review's
// Blocker 1 head-on: the old `requestsHelp` check scanned every word of
// EVERY guarded command for a bare --help/-h and, if found anywhere, skipped
// every check for that whole line — including the canonical-clone
// file-mutator guard and the gh pr merge refusal, neither of which has
// anything to do with wb#493's read-only --help/-h/help carve-out. Two
// concrete shapes from the review:
//
//   - `gh pr merge 123 --subject --help` is a REAL merge, not a help
//     request: --subject takes the next token unconditionally as its value,
//     so gh never sees --help as a flag at all (see gh.go's
//     ghPrMergeValueFlags).
//   - `-h` is not "help" on rsync (human-readable sizes) or BSD chmod/
//     chown/cp (operate on the symlink itself); it is an ordinary flag that
//     must never turn off the canonical-clone guard for those tools.
func TestBashHelpTokenNeverBypassesAnUnrelatedGuard(t *testing.T) {
	repositories := newFixture(t)
	target := filepath.Join(repositories.Canonical, "spec", "lessons", "x.md")
	commands := []struct {
		name    string
		command string
	}{
		{"gh pr merge with --help swallowed as --subject's value", "gh pr merge 123 --subject --help"},
		{"gh pr merge with -h swallowed as -t's value", "gh pr merge 123 -t -h"},
		{"gh pr merge with --help swallowed as --body's value", "gh pr merge 123 --body --help --squash"},
		{"gh pr merge with --help swallowed by the clustered -st", "gh pr merge 123 -st --help"},
		{"gh pr merge with --help after the -- terminator, a positional argument", "gh pr merge -- --help"},
		{"rm -rf with a trailing -h", "rm -rf " + target + " -h"},
		{"rsync -h, whose real meaning is human-readable sizes", "rsync -h " + target + " /tmp/out"},
		{"chmod -h, whose real meaning is operate on the symlink", "chmod -h 0644 " + target},
		{"chown -h, whose real meaning is operate on the symlink", "chown -h me:me " + target},
		{"cp -h, whose real meaning is operate on the symlink", "cp -h /tmp/src " + target},
	}
	for _, testCase := range commands {
		t.Run(testCase.name, func(t *testing.T) {
			decision := Inspect(bashCall(testCase.command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
			if !decision.Deny {
				t.Fatalf("Inspect(%q) allowed a real write/merge because of an unrelated -h/--help token", testCase.command)
			}
		})
	}
}

// TestBashSpecscoreCallerFlagBeforeHelpIsRefusedAsAWrite pins the wb#500
// second review's Should-fix 1, specscore half: the pre-fix requestsHelp
// scanned every word for a bare --help/-h and stopped there, so
// `specscore feature change-status <id> --caller --help --to Approved` was
// waved through as a help request even though --caller is a value-taking
// flag that consumes the very next token — the literal string "--help" —
// as its own value. Confirmed against the real specscore binary in the
// review: the call gets past flag parsing and fails only on project lookup,
// never printing help, proving it is a genuine change-status write. Now that
// requestsHelp requires a bare trailing --help/-h with no other flag on the
// line, this is inspected normally and refused exactly like any other
// change-status call with the clone as the working directory.
func TestBashSpecscoreCallerFlagBeforeHelpIsRefusedAsAWrite(t *testing.T) {
	repositories := newFixture(t)
	commands := []string{
		"specscore feature change-status wb-land-discoverability --caller --help --to Approved",
		"specscore feature change-status wb-land-discoverability --caller=--help --to Approved",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
			if !decision.Deny {
				t.Fatalf("Inspect(%q) allowed a real change-status write because --help sat on the line; want deny", command)
			}
			if !strings.Contains(decision.Reason, "change-status") {
				t.Fatalf("refusal for %q does not name the change-status write:\n%s", command, decision.Reason)
			}
		})
	}
}

// TestBashPackageManagerRunScriptDashDashHelpStillGovernedValidation pins the
// wb#500 second review's Should-fix 1, npm-family half: the pre-fix
// requestsHelp treated `npm run build -- --help` as a help request because
// --help appeared on the line at all, but a `--` separator hands everything
// after it to the script npm/pnpm invokes, not to npm/pnpm itself — the
// script actually runs. Confirmed against the real npm and pnpm binaries in
// the review: a script that touches a marker file on execution left the
// marker behind, proving the build ran for real. Now that requestsHelp
// refuses to recognise a line with a `--` separator as help, this is
// inspected normally and still hits the governed-validation gate inside a
// managed worktree, the same as a bare `npm run build`.
func TestBashPackageManagerRunScriptDashDashHelpStillGovernedValidation(t *testing.T) {
	repositories := newFixture(t)
	manifest := filepath.Join(repositories.Worktree, ".wb", "local", "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("schema_version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commands := []string{
		"npm run build -- --help",
		"pnpm run build -- --help",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			decision := Inspect(bashCall(command, repositories.Worktree), Options{ProjectsRoot: repositories.ProjectsRoot})
			if !decision.Deny {
				t.Fatalf("Inspect(%q) allowed direct heavy validation because --help sat on the line; want the governed-validation gate", command)
			}
			for _, expected := range []string{"wb run --", "durable ID"} {
				if !strings.Contains(decision.Reason, expected) {
					t.Fatalf("refusal for %q is missing %q:\n%s", command, expected, decision.Reason)
				}
			}
		})
	}
}

// TestBashSubcommandBeforeHelpIsInspectedNormallyForPassThroughTools pins the
// wb#500 third review: go/npm/pnpm/yarn/bun each have at least one subcommand
// that passes positional arguments straight through to a script or program
// instead of stopping at their own flag parser, so a subcommand word in front
// of --help/-h is not a safe help shape for them the way it is for
// cobra-based specscore. Confirmed against the real binaries: `pnpm run
// build --help` and `bun run build --help` ran the build script, and `go run
// . --help` ran the program. pnpm, yarn, and bun also run a package.json
// script when invoked WITHOUT "run" (`pnpm build --help` runs the "build"
// script too), so bareStyleHelp trusts nothing but the bare top-level
// invocation for these five tools — see requestsHelp's own doc.
//
// Each pair here asserts the --help line lands on exactly the same side of
// the governed-validation gate as the same command without --help: `npm run
// build --help` is inspected normally and still hits the gate a bare `npm
// run build` does (uniformly with the others, even though the real npm
// prints help for that one and never runs the script — the point is one
// rule, not per-tool leniency); `go run . --help` is inspected normally and,
// like a bare `go run .`, does not hit the gate at all, because "run" is not
// one of go's governed verbs (test/vet/build).
func TestBashSubcommandBeforeHelpIsInspectedNormallyForPassThroughTools(t *testing.T) {
	repositories := newFixture(t)
	manifest := filepath.Join(repositories.Worktree, ".wb", "local", "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("schema_version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pairs := []struct{ withHelp, withoutHelp string }{
		{"pnpm run build --help", "pnpm run build"},
		{"bun run build --help", "bun run build"},
		{"yarn build --help", "yarn build"},
		{"pnpm build --help", "pnpm build"},
		{"go run . --help", "go run ."},
		{"go test ./... -h", "go test ./..."},
		{"npm run build --help", "npm run build"},
	}
	for _, pair := range pairs {
		t.Run(pair.withHelp, func(t *testing.T) {
			withHelp := Inspect(bashCall(pair.withHelp, repositories.Worktree), Options{ProjectsRoot: repositories.ProjectsRoot})
			withoutHelp := Inspect(bashCall(pair.withoutHelp, repositories.Worktree), Options{ProjectsRoot: repositories.ProjectsRoot})
			if withHelp.Deny != withoutHelp.Deny {
				t.Fatalf("Inspect(%q).Deny = %v but Inspect(%q).Deny = %v; --help must not change which side of the governed-validation gate the call lands on", pair.withHelp, withHelp.Deny, pair.withoutHelp, withoutHelp.Deny)
			}
			if withHelp.Deny {
				for _, expected := range []string{"wb run --", "durable ID"} {
					if !strings.Contains(withHelp.Reason, expected) {
						t.Fatalf("refusal for %q is missing %q:\n%s", pair.withHelp, expected, withHelp.Reason)
					}
				}
			}
		})
	}
}

// TestHelpBypassNeverAppliesOutsideItsOwnAllowlist pins the scoping half of
// the Blocker 1 fix: the wb#493 --help/-h/help carve-out only ever applies to
// the specscore/go/npm-family tools it was built for (helpBypassTools), never
// to gh (which has its own, value-flag-aware recognition in gh.go) or to any
// file mutator.
func TestHelpBypassNeverAppliesOutsideItsOwnAllowlist(t *testing.T) {
	if helpBypassTools["gh"] {
		t.Fatal("helpBypassTools must never include gh: its --help/-h recognition belongs in gh.go's ghRequestsHelp, value-flag aware")
	}
	for name := range fileMutators {
		if helpBypassTools[name] {
			t.Fatalf("helpBypassTools must never include the file mutator %q", name)
		}
	}
	for _, name := range []string{"sed", "gsed", "perl", "ruby"} {
		if helpBypassTools[name] {
			t.Fatalf("helpBypassTools must never include the in-place editor %q", name)
		}
	}
}

// TestBashUnwrapsShellDashC pins the wb#500 review's Blocker 2: gh.go's doc
// comment, ai/skills/wb-hooks/SKILL.md and ai/capabilities.json all claimed
// the gh pr merge refusal reaches "chained, subshelled" invocations
// everywhere, but `bash -c "gh pr merge 123"` read as one opaque quoted word
// and was never unwrapped — commandWords/inspectCommand never saw a
// recognisable `gh`. Covers single and double quotes, a payload nested once
// (bash -c wrapping another bash -c), a chain inside the payload, and the
// `-lc` spelling agent harnesses commonly use for a login shell running one
// command.
func TestBashUnwrapsShellDashC(t *testing.T) {
	repositories := newFixture(t)
	refused := []struct {
		name    string
		command string
	}{
		{"bash -c, double-quoted payload", `bash -c "gh pr merge 123"`},
		{"bash -c, single-quoted payload", `bash -c 'gh pr merge 123'`},
		{"sh -c, double-quoted payload", `sh -c "gh pr merge 123"`},
		{"zsh -c, double-quoted payload", `zsh -c "gh pr merge 123"`},
		{"bash -lc, the login-shell spelling agent harnesses use", `bash -lc "gh pr merge 123"`},
		{"nested once: bash -c wrapping another bash -c", `bash -c "bash -c 'gh pr merge 123'"`},
		{"chained with && inside the payload", `bash -c "gh pr view 123 && gh pr merge 123"`},
		{"chained with ; inside the payload", `bash -c "gh pr view 123; gh pr merge 123"`},
		{"a canonical-clone write inside the payload, not only gh", `bash -c "git reset --hard"`},
		// wb#500 final review, S2: the payload is the first word after the
		// option words, not the word after -c. Every shape here ran a marker
		// payload on the real shell.
		{"bash -c -e: an option word after -c", `bash -c -e 'gh pr merge 123'`},
		{"bash -c --", `bash -c -- 'gh pr merge 123'`},
		{"bash -c -: a lone dash ends the options", `bash -c - 'gh pr merge 123'`},
		{"sh -c -x", `sh -c -x 'gh pr merge 123'`},
		{"sh -c +x", `sh -c +x 'gh pr merge 123'`},
		{"zsh -c -e", `zsh -c -e 'gh pr merge 123'`},
		{"bash -o pipefail -c", `bash -o pipefail -c 'gh pr merge 123'`},
		{"bash -c -o pipefail", `bash -c -o pipefail 'gh pr merge 123'`},
		{"bash +o errexit -c", `bash +o errexit -c 'gh pr merge 123'`},
		{"bash -O extglob -c", `bash -O extglob -c 'gh pr merge 123'`},
		{"bash -c -O extglob", `bash -c -O extglob 'gh pr merge 123'`},
		{"zsh -O is a flag, not a shopt name", `zsh -O -c 'gh pr merge 123'`},
		{"zsh -oerrexit: an attached option name", `zsh -oerrexit -c 'gh pr merge 123'`},
		{"ksh -oerrexit", `ksh -oerrexit -c 'gh pr merge 123'`},
		{"sh -oerrexit: sh may be zsh", `sh -oerrexit -c 'gh pr merge 123'`},
		{"bash -ceo pipefail: an o inside a cluster", `bash -ceo pipefail 'gh pr merge 123'`},
		{"bash -eco pipefail", `bash -eco pipefail 'gh pr merge 123'`},
		{"bash +c", `bash +c 'gh pr merge 123'`},
		{"bash --norc -c", `bash --norc -c 'gh pr merge 123'`},
		{"bash --rcfile file -c", `bash --rcfile /dev/null -c 'gh pr merge 123'`},
		{"dash -c", `dash -c 'gh pr merge 123'`},
		{"dash -c -e", `dash -c -e 'gh pr merge 123'`},
		{"ksh -c", `ksh -c 'gh pr merge 123'`},
		{"words after the payload are $0 and $1", `bash -c 'gh pr merge 123' arg0 arg1`},
		{"zsh -c -b", `zsh -c -b 'gh pr merge 123'`},
		{"an absolute shell path", `/bin/bash -c 'gh pr merge 123'`},
		{"a wrapper with options before the shell", `sudo -u alex bash -c 'gh pr merge 123'`},
		{"a loop inside the payload", `bash -c 'for n in 1 2; do gh pr merge "$n"; done'`},
		{"wb run -- bash -c", `wb run -- bash -c 'gh pr merge 123'`},
	}
	for _, testCase := range refused {
		t.Run(testCase.name, func(t *testing.T) {
			decision := Inspect(bashCall(testCase.command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
			if !decision.Deny {
				t.Fatalf("Inspect(%q) allowed a call wrapped in a shell -c payload; want deny", testCase.command)
			}
		})
	}

	allowed := []string{
		`bash -c "gh pr view 123"`,
		`bash -c "echo gh pr merge 123"`,
		`bash -o pipefail -c "gh pr view 123"`,
		`bash -c -e "gh pr merge --help"`,
	}
	for _, command := range allowed {
		t.Run(command, func(t *testing.T) {
			if decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot}); decision.Deny {
				t.Fatalf("Inspect(%q) refused a legitimate call inside a shell -c payload:\n%s", command, decision.Reason)
			}
		})
	}
}

// TestShellDashCPayloadEdgeCases covers shellDashCPayloads directly, per
// shell, for the option shapes the real shells were probed with (wb#500 final
// review, S2). The payload is the first word after the option words, not the
// word after -c, and bash, zsh and sh read -o/-O differently. A nil want means
// the shell has no payload to run: a script file, a missing payload, or an
// option that spent the -c word as its own value, in which case the real
// shell fails without running anything.
func TestShellDashCPayloadEdgeCases(t *testing.T) {
	const payload = "gh pr merge 123"
	cases := []struct {
		name  string
		words []string
		want  []string
	}{
		{"no -c token at all", []string{"bash", "script.sh"}, nil},
		{"-c is the last word, no payload follows", []string{"bash", "-c"}, nil},
		{"a script file before -c runs the script", []string{"bash", "script.sh", "-c", payload}, nil},
		{"bundled -lc", []string{"bash", "-lc", payload}, []string{payload}},
		{"an option word after -c", []string{"bash", "-c", "-e", payload}, []string{payload}},
		{"-- after -c", []string{"bash", "-c", "--", payload}, []string{payload}},
		{"a lone - after -c", []string{"bash", "-c", "-", payload}, []string{payload}},
		{"+x after -c", []string{"sh", "-c", "+x", payload}, []string{payload}},
		{"+c", []string{"bash", "+c", payload}, []string{payload}},
		{"-o and its value before -c", []string{"bash", "-o", "pipefail", "-c", payload}, []string{payload}},
		{"-o and its value after -c", []string{"bash", "-c", "-o", "pipefail", payload}, []string{payload}},
		{"+o and its value", []string{"bash", "+o", "errexit", "-c", payload}, []string{payload}},
		{"an o inside a cluster takes the next word", []string{"bash", "-ceo", "pipefail", payload}, []string{payload}},
		{"bash -O takes a shopt name", []string{"bash", "-O", "extglob", "-c", payload}, []string{payload}},
		{"bash -O spends -c as its value", []string{"bash", "-O", "-c", payload}, nil},
		{"bash -oerrexit spends -c as the option name", []string{"bash", "-oerrexit", "-c", payload}, nil},
		{"bash --rcfile takes a value", []string{"bash", "--rcfile", "/dev/null", "-c", payload}, []string{payload}},
		{"dash reads -o like bash", []string{"dash", "-o", "errexit", "-c", payload}, []string{payload}},
		{"zsh -O is a flag", []string{"zsh", "-O", "-c", payload}, []string{payload}},
		{"zsh -O extglob runs a script named extglob", []string{"zsh", "-O", "extglob", "-c", payload}, nil},
		{"zsh takes an attached -o value", []string{"zsh", "-oerrexit", "-c", payload}, []string{payload}},
		{"ksh takes an attached -o value", []string{"ksh", "-oerrexit", "-c", payload}, []string{payload}},
		{"sh is read both as bash and as zsh", []string{"sh", "-oerrexit", "-c", payload}, []string{payload}},
		{"words after the payload are $0 and $1", []string{"bash", "-c", payload, "arg0", "arg1"}, []string{payload}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := shellDashCPayloads(testCase.words, shellInterpreters[testCase.words[0]])
			if !slices.Equal(got, testCase.want) {
				t.Fatalf("shellDashCPayloads(%q) = %q, want %q", testCase.words, got, testCase.want)
			}
		})
	}
}

// TestBashAllowsAnInterpreterWithNoDashC covers the same shape through the
// public Inspect entry point: `bash script.sh` names no -c payload to
// recurse into, so it falls through like any other unrecognised command —
// this guard does not (and, per its own package doc, must not try to) read
// what a script file contains.
func TestBashAllowsAnInterpreterWithNoDashC(t *testing.T) {
	repositories := newFixture(t)
	if decision := Inspect(bashCall("bash script.sh", repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot}); decision.Deny {
		t.Fatalf("Inspect(%q) refused an interpreter invocation with no -c payload:\n%s", "bash script.sh", decision.Reason)
	}
}

// TestGhPrMergeOverrideEscapeHatchIsRecorded covers the explicit, recorded
// escape hatch as the refusal text now documents it: an inline
// `WB_AGENTGUARD_ALLOW_GH_PR_MERGE="<reason>"` assignment prefixing the exact
// `gh pr merge` call being rerun — directly, or via `env` — allows that call
// through and appends one audited line naming the command and the reason.
// wb#500 review, Should-fix 3: the pre-fix implementation read this override
// from the hook process's own ambient environment via os.Getenv, which the
// PreToolUse hook cannot receive scoped to one call — it runs in a process
// tree the Bash tool's shell never reaches — so an ambient value would have
// silently allowed every gh pr merge for the rest of a session instead of
// "the one call it is set on" the way both the refusal text and
// hooks_agent.go's doc comment promised. The ambient case below pins that it
// is no longer honoured at all.
func TestGhPrMergeOverrideEscapeHatchIsRecorded(t *testing.T) {
	repositories := newFixture(t)
	t.Run("no override still refuses", func(t *testing.T) {
		if decision := Inspect(bashCall("gh pr merge 1041", repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot}); !decision.Deny {
			t.Fatal("an unset override allowed gh pr merge through")
		}
	})
	t.Run("empty inline override still refuses", func(t *testing.T) {
		command := ghPrMergeOverrideEnv + `="" gh pr merge 1041`
		if decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot}); !decision.Deny {
			t.Fatal("an empty inline override allowed gh pr merge through")
		}
	})
	t.Run("whitespace-only inline override still refuses", func(t *testing.T) {
		command := ghPrMergeOverrideEnv + `="   " gh pr merge 1041`
		if decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot}); !decision.Deny {
			t.Fatal("a whitespace-only inline override allowed gh pr merge through")
		}
	})
	t.Run("an ambient environment override is never honoured", func(t *testing.T) {
		t.Setenv(ghPrMergeOverrideEnv, "an ambient value must never be read")
		if decision := Inspect(bashCall("gh pr merge 1041", repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot}); !decision.Deny {
			t.Fatal("an ambient environment override allowed gh pr merge through with no inline prefix")
		}
	})
	t.Run("a direct inline override is recorded and allows the call", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("WB_HOME", home)
		command := ghPrMergeOverrideEnv + `="wb worktree land refuses this exact receipt, escalating to sneat-dev/wb#999" gh pr merge 1041 --admin`
		decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
		if decision.Deny {
			t.Fatalf("a non-empty inline override still refused the call:\n%s", decision.Reason)
		}
		recorded, err := os.ReadFile(filepath.Join(home, "agentguard", "gh-pr-merge-overrides.jsonl"))
		if err != nil {
			t.Fatalf("read the override record: %v", err)
		}
		for _, expected := range []string{"land-with-wb-verb", "gh pr merge 1041 --admin", "escalating to sneat-dev/wb#999", "recorded_at"} {
			if !strings.Contains(string(recorded), expected) {
				t.Fatalf("override record does not contain %q:\n%s", expected, recorded)
			}
		}
	})
	t.Run("an override via env is honoured the same way", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("WB_HOME", home)
		command := "env " + ghPrMergeOverrideEnv + `="reason via env, sneat-dev/wb#999" gh pr merge 1041`
		decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
		if decision.Deny {
			t.Fatalf("an env-prefixed override still refused the call:\n%s", decision.Reason)
		}
		recorded, err := os.ReadFile(filepath.Join(home, "agentguard", "gh-pr-merge-overrides.jsonl"))
		if err != nil {
			t.Fatalf("read the override record: %v", err)
		}
		if !strings.Contains(string(recorded), "reason via env, sneat-dev/wb#999") {
			t.Fatalf("override record does not contain the env-prefixed reason:\n%s", recorded)
		}
	})
	// wb#500 second review, Nit 2, and final review, Nit 3: the override is
	// read through the same prefix walk that decides gh is the program
	// (stripCommandPrefixes), so a wrapper can never make the refusal see gh
	// while the override walk misses the assignment. The walk honours an
	// assignment only where the shell really exports it to gh: at the start
	// of the call, through env or sudo, or after a shell keyword. After any
	// other wrapper the shell cannot run the call at all ("nice:
	// VAR=reason: No such file or directory"), so the guard refuses it and
	// records nothing.
	honoured := []struct {
		name    string
		command string
		reason  string
	}{
		{"ahead of a wrapper", ghPrMergeOverrideEnv + `="reason ahead of nice, sneat-dev/wb#999" nice gh pr merge 1041`, "reason ahead of nice, sneat-dev/wb#999"},
		{"after env's own options", "env -u GH_DEBUG " + ghPrMergeOverrideEnv + `="reason after env -u, sneat-dev/wb#999" gh pr merge 1041`, "reason after env -u, sneat-dev/wb#999"},
		{"after sudo's own options", "sudo -u alex " + ghPrMergeOverrideEnv + `="reason after sudo -u, sneat-dev/wb#999" gh pr merge 1041`, "reason after sudo -u, sneat-dev/wb#999"},
		{"inside a loop body", `for n in 1041; do ` + ghPrMergeOverrideEnv + `="reason in a loop, sneat-dev/wb#999" gh pr merge "$n"; done`, "reason in a loop, sneat-dev/wb#999"},
		{"ahead of wb run --", ghPrMergeOverrideEnv + `="reason ahead of wb run, sneat-dev/wb#999" wb run -- gh pr merge 1041`, "reason ahead of wb run, sneat-dev/wb#999"},
	}
	for _, testCase := range honoured {
		t.Run("an override "+testCase.name+" is honoured and recorded", func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("WB_HOME", home)
			decision := Inspect(bashCall(testCase.command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
			if decision.Deny {
				t.Fatalf("Inspect(%q) refused a call carrying an exported override:\n%s", testCase.command, decision.Reason)
			}
			recorded, err := os.ReadFile(filepath.Join(home, "agentguard", "gh-pr-merge-overrides.jsonl"))
			if err != nil {
				t.Fatalf("read the override record: %v", err)
			}
			if !strings.Contains(string(recorded), testCase.reason) {
				t.Fatalf("override record does not contain %q:\n%s", testCase.reason, recorded)
			}
		})
	}
	notExported := []string{
		"nice " + ghPrMergeOverrideEnv + `="reason nice cannot pass on" gh pr merge 1041`,
		"timeout 60 " + ghPrMergeOverrideEnv + `="reason timeout cannot pass on" gh pr merge 1041`,
		"wb run -- " + ghPrMergeOverrideEnv + `="reason wb run cannot pass on" gh pr merge 1041`,
		"echo 1041 | xargs " + ghPrMergeOverrideEnv + `="reason xargs cannot pass on" gh pr merge`,
	}
	for _, command := range notExported {
		t.Run("an override the shell never exports is refused and not recorded: "+command, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("WB_HOME", home)
			if decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot}); !decision.Deny {
				t.Fatalf("Inspect(%q) honoured an override the shell never puts in gh's environment", command)
			}
			if _, err := os.Stat(filepath.Join(home, "agentguard", "gh-pr-merge-overrides.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("a refused call left an override record (stat error: %v)", err)
			}
		})
	}
}
func TestManagedWorktreeRequiresGovernedHeavyValidation(t *testing.T) {
	repositories := newFixture(t)
	manifest := filepath.Join(repositories.Worktree, ".wb", "local", "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("schema_version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	denied := []string{
		"go test ./internal/worktrees",
		"go vet ./cmd/wb",
		"go build ./...",
		"golangci-lint run ./...",
		"pnpm test",
		"pnpm run build:prod",
		"npm run lint",
		"yarn e2e",
		"npx nx affected --target=test",
		"cargo test --workspace",
		"cd internal && go test ./runlog",
		"timeout 600 go test ./...",
		"nice -n 10 go test ./...",
		`for p in ./a ./b; do go test "$p"; done`,
		"if true; then go vet ./...; fi",
	}
	for _, command := range denied {
		t.Run(command, func(t *testing.T) {
			decision := Inspect(bashCall(command, repositories.Worktree), Options{ProjectsRoot: repositories.ProjectsRoot})
			if !decision.Deny {
				t.Fatalf("Inspect(%q) allowed direct heavy validation", command)
			}
			for _, expected := range []string{"wb run --", "durable ID", "gofmt", "Prettier"} {
				if !strings.Contains(decision.Reason, expected) {
					t.Fatalf("refusal for %q is missing %q:\n%s", command, expected, decision.Reason)
				}
			}
		})
	}
}

func TestManagedWorktreeAllowsImmediateFormattingAndGovernedCommands(t *testing.T) {
	repositories := newFixture(t)
	manifest := filepath.Join(repositories.Worktree, ".wb", "local", "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("schema_version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	allowed := []string{
		"gofmt -w internal/runlog/runlog.go",
		"prettier --write web/src/app.ts",
		"wb run -- go test ./internal/runlog",
		"wb run -- pnpm test",
		"go env GOMODCACHE",
		"pnpm install",
		"wb run --quiet -- go test ./internal/runlog",
		"wb run -- timeout 600 go test ./...",
		"wb run -- bash -c 'go vet ./... && go test ./...'",
		"wb --projects-root /tmp/projects run -- go test ./...",
		"command -v go",
	}
	for _, command := range allowed {
		t.Run(command, func(t *testing.T) {
			if decision := Inspect(bashCall(command, repositories.Worktree), Options{ProjectsRoot: repositories.ProjectsRoot}); decision.Deny {
				t.Fatalf("Inspect(%q) refused an allowed command:\n%s", command, decision.Reason)
			}
		})
	}

	// A linked fixture without WB's manifest is outside this policy. The hook
	// must not claim authority over worktrees owned by another tool.
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	if decision := Inspect(bashCall("go test ./...", repositories.Worktree), Options{ProjectsRoot: repositories.ProjectsRoot}); decision.Deny {
		t.Fatalf("an unmanaged linked worktree was refused:\n%s", decision.Reason)
	}
}

// TestFileToolsAreJudgedByTheirPath covers Write, Edit, and the read tools
// that carry the same key and must not be touched.
func TestFileToolsAreJudgedByTheirPath(t *testing.T) {
	repositories := newFixture(t)
	inCanonical := filepath.Join(repositories.Canonical, "spec", "lessons", "note.md")
	inWorktree := filepath.Join(repositories.Worktree, "spec", "lessons", "note.md")
	cases := []struct {
		name string
		tool string
		path string
		key  string
		deny bool
	}{
		{"Write into a canonical clone", "Write", inCanonical, "file_path", true},
		{"Edit inside a canonical clone", "Edit", inCanonical, "file_path", true},
		{"MultiEdit inside a canonical clone", "MultiEdit", inCanonical, "file_path", true},
		{"NotebookEdit inside a canonical clone", "NotebookEdit", inCanonical, "notebook_path", true},
		{"Write inside a worktree", "Write", inWorktree, "file_path", false},
		{"Edit inside a worktree", "Edit", inWorktree, "file_path", false},
		{"Read of a canonical clone", "Read", inCanonical, "file_path", false},
		{"NotebookRead of a canonical clone", "NotebookRead", inCanonical, "notebook_path", false},
		{"Glob over a canonical clone", "Glob", inCanonical, "file_path", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			call := ToolCall{
				HookEventName: "PreToolUse",
				ToolName:      testCase.tool,
				ToolInput:     json.RawMessage(`{"` + testCase.key + `":` + mustJSON(testCase.path) + `}`),
			}
			decision := Inspect(call, Options{ProjectsRoot: repositories.ProjectsRoot})
			if decision.Deny != testCase.deny {
				t.Fatalf("Inspect(%s %s).Deny = %v, want %v", testCase.tool, testCase.path, decision.Deny, testCase.deny)
			}
		})
	}
}

// TestGuardFailsOpen is the property the whole design is subordinate to. Every
// case here would, if it denied, stop every agent on the machine.
func TestGuardFailsOpen(t *testing.T) {
	repositories := newFixture(t)
	write := `{"command":` + mustJSON("git reset --hard") + `}`
	cases := []struct {
		name    string
		call    ToolCall
		options Options
	}{
		{
			name:    "no projects root configured",
			call:    bashCall("git reset --hard", repositories.Canonical),
			options: Options{},
		},
		{
			name:    "an empty payload",
			call:    ToolCall{},
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
		{
			name:    "a different hook event",
			call:    ToolCall{HookEventName: "PostToolUse", ToolName: "Bash", CWD: repositories.Canonical, ToolInput: json.RawMessage(write)},
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
		{
			name:    "a tool name WB has never heard of",
			call:    ToolCall{HookEventName: "PreToolUse", ToolName: "mcp__something__mutate", CWD: repositories.Canonical, ToolInput: json.RawMessage(write)},
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
		{
			name:    "a tool_input that is not an object",
			call:    ToolCall{HookEventName: "PreToolUse", ToolName: "Bash", CWD: repositories.Canonical, ToolInput: json.RawMessage(`"git reset --hard"`)},
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
		{
			name:    "a tool_input holding the wrong types",
			call:    ToolCall{HookEventName: "PreToolUse", ToolName: "Bash", CWD: repositories.Canonical, ToolInput: json.RawMessage(`{"command":42}`)},
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
		{
			name:    "no tool_input at all",
			call:    ToolCall{HookEventName: "PreToolUse", ToolName: "Bash", CWD: repositories.Canonical},
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
		{
			name:    "an empty command",
			call:    bashCall("", repositories.Canonical),
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
		{
			name:    "a command that is only whitespace and operators",
			call:    bashCall("&& || ; | ( ) { }", repositories.Canonical),
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
		{
			name:    "an unterminated quote around a whole command",
			call:    bashCall(`echo "git reset --hard`, repositories.Canonical),
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
		{
			name:    "a quoted string standing alone",
			call:    bashCall(`"git reset --hard"`, repositories.Canonical),
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
		{
			name:    "an unterminated heredoc",
			call:    bashCall("cat <<'EOF'\ngit reset --hard", repositories.Canonical),
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
		{
			name:    "a projects root that does not exist",
			call:    bashCall("git reset --hard", repositories.Canonical),
			options: Options{ProjectsRoot: filepath.Join(t.TempDir(), "absent")},
		},
		{
			name:    "no working directory",
			call:    bashCall("git reset --hard", ""),
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
		{
			name:    "a relative working directory",
			call:    bashCall("git reset --hard", "some/relative/path"),
			options: Options{ProjectsRoot: repositories.ProjectsRoot},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if decision := Inspect(testCase.call, testCase.options); decision.Deny {
				t.Fatalf("the guard failed closed:\n%s", decision.Reason)
			}
		})
	}
}

// TestDecodeToolCallFailsOpenOnUnreadablePayloads proves the decode step
// cannot manufacture a refusal out of nonsense.
func TestDecodeToolCallFailsOpenOnUnreadablePayloads(t *testing.T) {
	repositories := newFixture(t)
	payloads := []string{
		"",
		"not json at all",
		"[]",
		"null",
		`{"tool_name":`,
		`{"tool_name":"Bash","tool_input":{"command":"git reset --hard"`,
	}
	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			call := DecodeToolCall(strings.NewReader(payload))
			if decision := Inspect(call, Options{ProjectsRoot: repositories.ProjectsRoot}); decision.Deny {
				t.Fatalf("payload %q produced a refusal:\n%s", payload, decision.Reason)
			}
		})
	}
}

// TestDecodeToolCallReadsARealPayload uses the documented Claude Code shape
// verbatim. A wrong field name here means the guard never fires at all, which
// is the one failure that looks exactly like success.
func TestDecodeToolCallReadsARealPayload(t *testing.T) {
	repositories := newFixture(t)
	payload := `{
	  "session_id": "abc123",
	  "transcript_path": "/home/user/.claude/projects/x/transcript.jsonl",
	  "cwd": ` + mustJSON(repositories.Canonical) + `,
	  "permission_mode": "default",
	  "hook_event_name": "PreToolUse",
	  "tool_name": "Bash",
	  "tool_input": {
	    "command": "git checkout origin/main -- .",
	    "description": "Restore the tree",
	    "timeout": 120000,
	    "run_in_background": false
	  },
	  "tool_use_id": "toolu_01ABC123"
	}`
	call := DecodeToolCall(strings.NewReader(payload))
	if call.ToolName != "Bash" || call.CWD != repositories.Canonical || call.HookEventName != "PreToolUse" {
		t.Fatalf("decoded payload lost a field: %+v", call)
	}
	decision := Inspect(call, Options{ProjectsRoot: repositories.ProjectsRoot})
	if !decision.Deny {
		t.Fatal("the documented payload shape did not reach a refusal")
	}
}

// TestWriteDecisionEmitsNothingForAnAllow keeps silence as the allow signal.
// Emitting an explicit "allow" would suppress the permission prompt the user
// would otherwise have seen.
func TestWriteDecisionEmitsNothingForAnAllow(t *testing.T) {
	var buffer bytes.Buffer
	wrote, err := WriteDecision(&buffer, Decision{})
	if err != nil {
		t.Fatalf("WriteDecision: %v", err)
	}
	if wrote || buffer.Len() != 0 {
		t.Fatalf("an allow wrote %q", buffer.String())
	}
}

// TestWriteDecisionEmitsTheDocumentedDenyShape pins the exact response schema.
func TestWriteDecisionEmitsTheDocumentedDenyShape(t *testing.T) {
	var buffer bytes.Buffer
	if _, err := WriteDecision(&buffer, Decision{Deny: true, Reason: "because"}); err != nil {
		t.Fatalf("WriteDecision: %v", err)
	}
	var document struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &document); err != nil {
		t.Fatalf("the deny document is not valid JSON: %v\n%s", err, buffer.String())
	}
	output := document.HookSpecificOutput
	if output.HookEventName != "PreToolUse" || output.PermissionDecision != "deny" || output.PermissionDecisionReason != "because" {
		t.Fatalf("unexpected deny document: %+v", output)
	}
	if !strings.HasPrefix(buffer.String(), "{") {
		t.Fatal("Claude Code only parses stdout that begins with {")
	}
}

// TestSplitSegmentsSkipsHeredocBodies protects the one construct that would
// otherwise let a data payload be read as commands, in either direction.
func TestSplitSegmentsSkipsHeredocBodies(t *testing.T) {
	segments := splitSegments("cat > out.txt <<'EOF'\ngit reset --hard\nrm -rf /\nEOF\necho done")
	var commands []string
	for _, current := range segments {
		if len(current.Words) > 0 {
			commands = append(commands, strings.Join(current.Words, " "))
		}
	}
	joined := strings.Join(commands, " | ")
	if strings.Contains(joined, "reset") || strings.Contains(joined, "rm") {
		t.Fatalf("a heredoc body was read as shell: %q", joined)
	}
	if !strings.Contains(joined, "echo done") {
		t.Fatalf("the command after the heredoc was lost: %q", joined)
	}
	if len(segments) == 0 || len(segments[0].RedirectTargets) != 1 || segments[0].RedirectTargets[0] != "out.txt" {
		t.Fatalf("the heredoc's redirection target was lost: %+v", segments)
	}
}

// TestCommandWordsSeesThroughPrefixes keeps leading assignments, shell
// keywords and wrapper programs, with their own options and values, from
// hiding the program that follows (wb#500 final review, S3). It also pins
// what is NOT a prefix: a `for` word list, `wb` without run --, and a
// recipe-mode `wb run`.
func TestCommandWordsSeesThroughPrefixes(t *testing.T) {
	cases := []struct {
		command  string
		want     string
		governed bool
	}{
		{"sudo rm -rf x", "rm -rf x", false},
		{"FOO=bar git reset", "git reset", false},
		{"env FOO=bar git reset", "git reset", false},
		{"time git reset", "git reset", false},
		{"time -p git reset", "git reset", false},
		{"/usr/bin/time -o out git reset", "git reset", false},
		{"sudo -u alex -g staff rm x", "rm x", false},
		{"sudo --user alex rm x", "rm x", false},
		{"sudo -u alex FOO=bar rm x", "rm x", false},
		{"nice -n 10 git reset", "git reset", false},
		{"nice -n10 git reset", "git reset", false},
		{"nice -10 git reset", "git reset", false},
		{"stdbuf -oL git reset", "git reset", false},
		{"stdbuf -o L git reset", "git reset", false},
		{"exec -a name git reset", "git reset", false},
		{"command -p git reset", "git reset", false},
		{"builtin command git reset", "git reset", false},
		{"nohup git reset", "git reset", false},
		{"timeout 60 git reset", "git reset", false},
		{"timeout -s KILL -k 5 60 git reset", "git reset", false},
		{"timeout --kill-after=5 60 git reset", "git reset", false},
		{"caffeinate -i git reset", "git reset", false},
		{"caffeinate -t 5 git reset", "git reset", false},
		{"xargs git reset", "git reset", false},
		{"xargs -n 1 git reset", "git reset", false},
		{"xargs -I {} git reset", "git reset", false},
		{"xargs -0 -P 4 git reset", "git reset", false},
		{"env -u X git reset", "git reset", false},
		{"env -i FOO=bar git reset", "git reset", false},
		{"env -- git reset", "git reset", false},
		{"do git reset", "git reset", false},
		{"then git reset", "git reset", false},
		{"else git reset", "git reset", false},
		{"elif git reset", "git reset", false},
		{"if ! git reset", "git reset", false},
		{"while git reset", "git reset", false},
		{"until git reset", "git reset", false},
		{"coproc git reset", "git reset", false},
		{"sudo -u alex nice -n 5 timeout 60 git reset", "git reset", false},
		{"wb run -- git reset", "git reset", true},
		{"wb run --quiet -- git reset", "git reset", true},
		{"wb --projects-root /p run -- git reset", "git reset", true},
		{"nice wb run -- git reset", "git reset", true},
		{"for n in git reset", "for n in git reset", false},
		{"wb run refresh-ci", "wb run refresh-ci", false},
		{"wb worktree land .", "wb worktree land .", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.command, func(t *testing.T) {
			stripped := stripCommandPrefixes(strings.Fields(testCase.command))
			if got := strings.Join(stripped.Words, " "); got != testCase.want || stripped.Governed != testCase.governed {
				t.Fatalf("stripCommandPrefixes(%q) = %q (governed %v), want %q (governed %v)", testCase.command, got, stripped.Governed, testCase.want, testCase.governed)
			}
		})
	}
}

// TestBashSeesThroughKeywordsAndWrapperOptionsForEveryChecker pins that the
// wider prefix walk (wb#500 final review, S3) serves every recogniser, not
// only gh's. `sudo -u x rm ...` in a canonical clone was allowed before it.
func TestBashSeesThroughKeywordsAndWrapperOptionsForEveryChecker(t *testing.T) {
	repositories := newFixture(t)
	target := filepath.Join(repositories.Canonical, "README.md")
	commands := []string{
		"sudo -u alex rm -rf " + target,
		"nice -n 10 rm " + target,
		"if true; then git reset --hard; fi",
		"for n in 1; do git reset --hard; done",
		"timeout 60 git reset --hard",
		"echo x | xargs -I{} rm " + target,
		"wb run -- go mod tidy",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			if decision := Inspect(bashCall(command, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot}); !decision.Deny {
				t.Fatalf("Inspect(%q) allowed a canonical-clone write behind a keyword or wrapper", command)
			}
		})
	}
}

// TestRefusalNamesTheRemedy keeps the message actionable. A refusal an agent
// cannot act on is a refusal it works around.
func TestRefusalNamesTheRemedy(t *testing.T) {
	repositories := newFixture(t)
	decision := Inspect(bashCall("git reset --hard", repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
	if !decision.Deny {
		t.Fatal("expected a refusal")
	}
	for _, expected := range []string{
		repositories.Canonical,
		"wb worktree create <task> sneat-co/backstage",
		"wb worktree rescue",
		"git reset",
	} {
		if !strings.Contains(decision.Reason, expected) {
			t.Fatalf("refusal is missing %q:\n%s", expected, decision.Reason)
		}
	}
}
