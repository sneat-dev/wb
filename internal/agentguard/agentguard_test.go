package agentguard

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
		{"hooks bypass, the commit that landed anyway", `git -c core.hooksPath=/dev/null commit -q -m x`, repositories.Canonical},
		{"hooks bypass by any casing", `git -c CORE.HOOKSPATH=/dev/null commit -m x`, repositories.Canonical},
		{"no-verify bypass", "git push --no-verify origin main", repositories.Canonical},
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

// TestCommandWordsSeesThroughPrefixes keeps `sudo`, `env FOO=bar`, and a bare
// assignment from hiding the program that follows.
func TestCommandWordsSeesThroughPrefixes(t *testing.T) {
	cases := map[string][]string{
		"sudo rm -rf x":         {"sudo", "rm", "-rf", "x"},
		"FOO=bar git reset":     {"FOO=bar", "git", "reset"},
		"env FOO=bar git reset": {"env", "FOO=bar", "git", "reset"},
		"time git reset":        {"time", "git", "reset"},
	}
	for name, words := range cases {
		t.Run(name, func(t *testing.T) {
			got := commandWords(words)
			if len(got) == 0 || (got[0] != "rm" && got[0] != "git") {
				t.Fatalf("commandWords(%v) = %v", words, got)
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
