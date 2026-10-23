package cmdhooks

import (
	"github.com/sneat-dev/wb/internal/agentguard"
	"github.com/spf13/cobra"
)

// newHooksAgentCmd groups the hooks that an AI coding agent runs around its
// own tool calls, as opposed to the Git hooks the rest of `wb hooks` installs.
//
// They sit under the same noun because they answer the same question at
// different moments: Git hooks judge a commit, agent hooks judge the write
// that would have produced it. The agent layer is the one that matters for a
// canonical clone, because a clone can be ruined without ever reaching a
// commit.
func (family commands) newHooksAgentCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "agent",
		Short: "Hooks an AI coding agent runs around its own tool calls",
	}
	command.AddCommand(family.newHooksAgentPreToolUseCmd())
	command.AddCommand(family.newHooksAgentInstallCmd())
	return command
}

func (family commands) newHooksAgentPreToolUseCmd() *cobra.Command {
	var inputPath string
	command := &cobra.Command{
		Use:   "pre-tool-use",
		Short: "Judge one agent tool call before it runs (reads a PreToolUse payload on stdin)",
		Long: `Refuse an agent tool call that violates one of this guard's policies.

Reads a Claude Code PreToolUse payload as JSON on stdin and writes a deny
document, or a rewrite document with no permission decision, on stdout when
the call matches one of the policies below. It writes nothing at all for
every other outcome.

Policies (Bash/Write/Edit/MultiEdit/NotebookEdit, unless noted):

  - Canonical-clone write: refuses a write inside <projects-root>/<owner>/
    <repository> — the canonical clone every linked worktree in the fleet is
    cut from.
  - Hook bypass: refuses '--no-verify'/'-n' on git commit/push/merge,
    'git -c core.hooksPath=...', and 'git config core.hooksPath', in any
    WB-managed checkout (canonical clone or linked worktree). See
    lesson work-preservation-is-never-grounds-to-bypass-a-hook.
  - Auto-tagging: refuses a hand-pushed 'git tag'/'git push --tags'/
    'git push origin <tag>' in a repository whose own CI already tags it
    (an explicit 'agent.autoTags: true' in '.wb/hooks.yaml', or a
    strongo/cicd reusable workflow with no 'disable-version-bumping: true').
    See lessons l3/l11.
  - Governed heavy validation: rewrites 'go test'/'golangci-lint'/etc. run
    directly inside a managed worktree into 'wb run -- ...' instead of
    refusing it (wb#637, founder decision 2026-09-18), but only for the one
    narrow shape wb#645's review restricted this to: a single governed
    command, optionally prefixed by 'VAR=value' assignments and/or a leading
    'cd <dir> &&'. The env assignments move in front of 'wb run' so they
    still apply ('GOOS=windows go build ./...' becomes
    'GOOS=windows wb run -- go build ./...'), and a leading 'cd' stays
    outside 'wb run --' so it still changes the shell's directory. Any other
    compound command ('&&' twice, ';', a pipeline, '||', a subshell, a
    heredoc) is never rewritten; it falls back to the pre-PR refusal that
    names the command to submit through 'wb run --' yourself. There is no
    'sh -c' wrapping anywhere: it hid the inner command from the user's own
    Bash permission rules, changed semantics under a non-bash /bin/sh, and
    could let a governed command hide a chained deny. Outside a managed
    worktree, and for a call already under 'wb run --', nothing changes.
    'time <command>' is treated as a compound shape and is never rewritten,
    because 'wb run -- time ...' would run /usr/bin/time instead of the
    shell's own 'time' keyword.

    A rewrite is written as 'updatedInput' with NO 'permissionDecision' at
    all — this is the guard's one exception to "only a deny is ever
    written". Claude Code v2.1.276 applies a response that sets
    'updatedInput' with no 'permissionDecision' as the new input and then
    runs its NORMAL permission flow on it, so the user's own prompts and
    allow/deny/ask rules still apply to the rewritten command exactly as
    they would to the one the agent proposed. This is deliberate: an
    explicit 'allow' would suppress the permission prompt entirely, which is
    not this guard's call to make, and this behaviour is undocumented and
    depends on Claude Code >= 2.1.276. Every other 'tool_input' field the
    call carried (description, timeout, run_in_background, ...) passes
    through unchanged. Every WB deny policy is evaluated across the WHOLE
    command line before a rewrite is even considered, ignoring any governed
    match while doing so, so a real deny anywhere on the line (a chained
    'gh pr merge', a canonical-clone write, ...) always wins over a rewrite.
  - Subagent-ID stamp: when the PreToolUse payload carries 'agent_id' (Claude
    Code sends it only from a subagent) and the Bash command is, entirely on
    its own, a simple invocation of 'wb' (a single command, no '&&'/';'/'|',
    optionally prefixed by 'VAR=value' assignments), prefixes
    'export WB_SUBAGENT_ID=<agent_id> WB_SUBAGENT_TOOL_USE_ID=<tool_use_id>;'
    onto it, so every WB record that call's 'wb' invocation writes carries
    the subagent and tool-call identity (wb#631's provenance fields). These
    names are deliberately outside the WB_AGENT_* family WB's own
    owner-identity variables use (WB_AGENT_ID already names the session that
    claims a worktree), so a subagent stamp is never mistaken for a
    declared owner identity by wb's own admission checks. Both IDs are
    validated against a compact safe charset before they are ever
    interpolated into the rewritten command; an unsafe or absent agent_id
    drops the whole prefix. A main-thread call (no 'agent_id'), or a Bash
    command that chains 'wb' with anything else, is never stamped, and this
    never sets 'permissionDecision' either.
  - Missing model (Agent/Task tool): refuses a subagent dispatch that names
    no 'model'. See lesson l49.
  - Literal report path (Agent/Task tool): refuses a dispatch prompt that
    hand-writes a WB report path instead of deriving it from
    'wb worktree log finalize --report'. See lesson
    a-report-path-hand-written-into-a-brief-diverges-from-wb-home-on-the-target-host.
  - Dispatch into a live claim (Agent/Task tool): refuses a dispatch that
    names a repository another live WB claim already covers. See lesson
    a-brief-was-dispatched-for-work-already-under-an-active-wb-claim.
  - Land with the WB verb: refuses every call gh itself would run as
    'pr merge', and names 'wb worktree land <worktree>' and
    'wb pr land <owner/repo#n>' instead. See rule land-with-wb-verb
    (sneat-co/backstage). gh's arguments are read the way gh's own command
    lookup reads them, so a flag before 'pr' or between 'pr' and 'merge'
    does not hide the call ('gh pr -R o/r merge 1', 'gh pr --squash 1
    merge'), while 'gh help pr merge', 'gh search issues pr merge' and
    'gh pr merge --help' are allowed. The call is found:
      - chained with '&&', '||', ';', '|' or a newline, or inside '( )',
        '{ }' or an unquoted '$( )';
      - in an if/elif/while/until condition, a then/else branch, a loop's
        'do' body, or after '!' or 'coproc';
      - behind VAR=value assignments, and behind sudo, env, nice, nohup,
        time, stdbuf, exec, command, builtin, noglob, nocorrect, timeout,
        caffeinate, xargs and 'wb run --', together with each wrapper's own
        options and their values;
      - in the -c payload of bash, sh, zsh, dash and ksh, taken the way
        that shell takes it (bounded depth);
      - through the shell's brace expansion of any word ('gh pr {merge,} 1'
        is 'gh pr merge 1' on bash, sh and zsh), and whatever the letter
        case of the program name ('Gh pr merge 1' runs gh on macOS).
    The 'gh api' routes that merge a pull request are refused under the
    same policy with the same escape hatch: a PUT to
    repos/<owner>/<repo>/pulls/<n>/merge and a GraphQL call naming the
    mergePullRequest mutation. GET .../merge, every other method and
    endpoint, a GraphQL query and 'gh api ... --help' stay allowed.
    A '#' that starts a word opens a comment, as bash reads it; nothing
    after it on that line is inspected ('ls # && gh pr merge 1' is allowed).
    Not inspected, so still allowed:
      - gh aliases and extensions, and a GraphQL mutation read from
        'gh api --input <file>';
      - scripts run from files, eval, here-strings, backticks, a quoted
        "$( )", ANSI-C $'...' quoting and 'env -S';
      - a word the shell builds by parameter or glob expansion
        ('gh pr ${X:-merge} 1', 'gh pr merge$X 1'): words are read
        literally, brace lists excepted;
      - other interpreters ('python3 -c', 'node -e') and wrappers not
        listed above ('ssh', 'watch', 'doas', 'parallel', 'find -exec');
      - 'git push' to the base branch, and 'hub merge'.
    Escape hatch: put WB_AGENTGUARD_ALLOW_GH_PR_MERGE="<reason>" on that
    exact call, where the shell really puts it into gh's environment: at
    the start of the call, or right after env, sudo, time or a shell
    keyword. It lets that one call through. The guard records the reason
    when it allows the call, which proves only that the guard allowed it,
    not that the call ran. The hook process's own ambient environment is
    never read, so a value set ahead of time cannot silently cover a whole
    session.
  - Masked exit status (Bash): refuses a state-changing wb verb ('create',
    'land', 'pr create', 'pr land', 'worktree create'/'land'/'merge', and a
    'worktree cleanup'/'gc' with --apply, among others) whose output is piped
    into another command ('|' or '|&', including '2>&1 |', through a path to
    wb, inside a subshell, '$( )' or a 'bash -c' payload). A pipeline reports
    only its last command's status, so the verb's refusal exits 0 and an '&&'
    chain runs on (sneat-dev/wb#813). Fix: drop the pipe and use '--quiet'
    (outcome and refusals only), capture-then-parse for '--format json', or
    'set -o pipefail' earlier in the same shell (a child 'bash -c' needs its
    own; a subshell or '&&' branch does not count; reading PIPESTATUS does
    not). A read-only verb, a verb that ends its pipeline, and any other
    program are allowed. Not seen through: eval, script, $WB, function
    wrappers, env -S, go run, renamed binaries, ssh. There is no override.

A 'specscore'/'go'/'npm'/'pnpm'/'yarn'/'bun' invocation is never refused for
naming a write verb when its own words are shaped as a genuine help request
(wb#493), but which shape counts depends on the tool. 'specscore' is
cobra-based, so a trailing '--help'/'-h' always prints help no matter how many
subcommand words precede it: a subcommand chain with no other flag at all,
trailing in exactly one '--help'/'-h' and nothing after it, or the literal
word 'help' first with no flag anywhere in the rest. 'go'/'npm'/'pnpm'/'yarn'/
'bun' get only the bare top-level shape — exactly '<tool> --help'/'<tool> -h'
with nothing else after the program name, or '<tool> help' followed by zero or
more non-flag words — because each has at least one subcommand that passes
positional arguments straight through to a script or program instead of
stopping at its own flag parser: against the real binaries, 'pnpm run build
--help' and 'bun run build --help' ran the build script, and 'go run . --help'
ran the program; pnpm/yarn/bun also run a package.json script when invoked
WITHOUT 'run' ('pnpm build --help' runs the 'build' script too), so no
denylist of pass-through verbs is safe for them either. Any other flag on the
line — one positioned to be swallowed as an earlier flag's own value
('specscore change-status <id> --caller --help --to Approved' really calls
change-status, because '--caller' takes the next token unconditionally as its
value), a '--' separator that hands '--help' to a script instead of the
wrapper ('npm run build -- --help' really runs the build script), or (for the
five non-cobra tools) any subcommand word at all in front of '--help'/'-h'
('npm run build --help', 'go test ./... -h') — is inspected normally instead,
which for these tools means it still hits the governed-validation gate inside
a managed worktree exactly like the same command without '--help' would.
gh pr merge's own '--help'/'-h' recognition is separate and reads the flags
the way gh does:
  - 'gh pr merge 123 --subject --help' and 'gh pr merge 123 -st --help' are
    real merges: '--subject', like the '-t' that ends '-st', takes the next
    token unconditionally as its value.
  - 'gh pr merge -- --help' is a real merge too, because '--' makes '--help'
    a positional argument.
  - Only a '--help'/'-h' that no value-taking flag consumed and that comes
    before any '--' is treated as help. It may stand alone or sit in a
    cluster of boolean short flags such as '-sh'.

This command fails open without exception. An unreadable payload, an
unrecognised tool, a shell construct it cannot model, a path it cannot
resolve, and an internal panic all produce silence, which Claude Code reads as
"proceed". It runs ahead of every tool call of every agent on the machine, so
a guard that could fail closed would be a worse defect than the one it exists
to prevent.

It never blocks a read, and never blocks a linked worktree's ordinary work —
including a worktree nested inside a canonical clone. In a canonical clone it
leaves the operations a clone exists for alone: git fetch, git merge
--ff-only, git status, git log, git show, git ls-tree, and every other read.

Install it with 'wb hooks agent install'.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := cmd.InOrStdin()
			if inputPath != "" && inputPath != "-" {
				file, err := family.agent.OpenInput(inputPath)
				if err != nil {
					// Even an explicitly named payload that cannot be read is
					// an allow: this command has exactly one safe failure.
					return nil
				}
				defer func() { _ = file.Close() }()
				reader = file
			}
			call := agentguard.DecodeToolCall(reader)
			decision := family.agent.Inspect(call, agentguard.Options{ProjectsRoot: family.runtime.Flags().ProjectsRoot, WBExecutable: family.agent.ResolveGovernor(family.agent.Executable())})
			if _, err := agentguard.WriteDecision(cmd.OutOrStdout(), decision, call.ToolInput); err != nil {
				return nil
			}
			return nil
		},
	}
	command.Flags().StringVar(&inputPath, "input", "", "read the payload from a file instead of stdin (for testing)")
	return command
}
