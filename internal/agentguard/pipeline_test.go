package agentguard

import (
	"reflect"
	"strings"
	"testing"
)

// maskedPipelineCommand is the exact command shape of the 2026-10-02 incident
// (sneat-dev/wb#813): a refused `wb worktree create` piped through `tail`, so
// the pipeline exited 0, the && chain carried on, and a duplicate pull request
// was opened.
const maskedPipelineCommand = "wb worktree create founder-rulings-1002b datatug/backstage --model claude-sonnet-5-5 " +
	"--original-prompt-file /tmp/prompt.md 2>&1 | tail -1 && cd /tmp/wt && echo edit >> notes.md && " +
	"git commit -am 'docs: edit' && wb pr create"

func inspectPipeline(t *testing.T, command string) Decision {
	t.Helper()
	return Inspect(bashCall(command, t.TempDir()), Options{ProjectsRoot: t.TempDir()})
}

// maskedPipelineHoles are the shapes the first review of wb#816 found allowed
// although they still hide a verb's status, and the verbs it found missing.
var maskedPipelineHoles = []struct {
	name    string
	command string
}{
	// Reading the status afterwards does not stop the chain that already ran.
	{"incident shape with a trailing PIPESTATUS read", "wb worktree create t o/r --original-prompt-file /tmp/p 2>&1 | tail -1 && cd /x && wb pr create; echo \"exit=${PIPESTATUS[0]}\""},
	{"PIPESTATUS only in a comment", "wb pr create | tail # PIPESTATUS"},
	{"pipestatus only in an echo", "wb pr create | tail -1 && echo pipestatus"},
	{"PIPESTATUS read on the next line", "wb pr create | tail -1\ntest ${PIPESTATUS[0]} -eq 0"},
	{"PIPESTATUS read after a semicolon", "wb pr create | tail -1; test ${PIPESTATUS[0]} -eq 0"},
	{"zsh pipestatus read", "wb pr create | tail -1; test ${pipestatus[1]} -eq 0"},

	// A child shell does not inherit the parent's pipefail.
	{"bash -c under a parent pipefail", "set -o pipefail; bash -c 'wb pr create | tail'"},
	{"zsh -c under a parent setopt", "setopt pipefail; zsh -c 'wb pr create | tail'"},
	{"sh -c under a parent pipefail", "set -euo pipefail; sh -c 'wb land . | tail'"},
	{"nested payload under a parent pipefail", "set -o pipefail; sh -c \"bash -c 'wb land . | tail'\""},
	{"payload shell option switched off again", "bash -o pipefail +o pipefail -c 'wb pr create | tail'"},
	{"payload option after the payload is a positional", "bash -c 'wb pr create | tail' -o pipefail"},
	{"a payload with its own pipefail does not carry it out", "bash -o pipefail -c true; wb pr create | tail"},

	// Pipefail scope and spelling.
	{"pipefail set in a subshell that closed", "(set -o pipefail); wb pr create | tail"},
	{"pipefail set in a substitution that closed", "$(set -o pipefail); wb pr create | tail"},
	{"pipefail set in a nested subshell that closed", "( (set -o pipefail) ; wb pr create | tail )"},
	{"pipefail set behind &&", "false && set -o pipefail; wb pr create | tail"},
	{"pipefail set behind ||", "true || set -o pipefail; wb pr create | tail"},
	{"pipefail set in a conditional group", "false && { set -o pipefail; }; wb pr create | tail"},
	{"pipefail set in a conditional subshell", "false || (set -o pipefail); wb pr create | tail"},
	{"pipefail set in a conditional group inside a group", "false && { { set -o pipefail; }; }; wb pr create | tail"},
	{"pipefail set behind then", "if true; then set -o pipefail; fi; wb pr create | tail"},
	{"pipefail set with an environment prefix", "FOO=1 set -o pipefail; wb pr create | tail"},
	{"pipefail set as one side of a pipe", "echo x | set -o pipefail; wb pr create | tail"},
	{"pipefail set on the left of a pipe", "set -o pipefail | cat; wb pr create | tail"},
	{"pipefail set in a piped group", "{ set -o pipefail; } | cat; wb pr create | tail"},
	{"pipefail switched off by setopt nopipefail", "setopt pipefail; setopt nopipefail; wb pr create | tail"},
	{"pipefail switched off by setopt NO_PIPE_FAIL", "set -o pipefail; setopt NO_PIPE_FAIL; wb pr create | tail"},
	{"pipefail switched off by unsetopt", "set -o pipefail; unsetopt pipefail; wb pr create | tail"},
	{"pipefail negated spelling is never on", "setopt nopipefail; wb pr create | tail"},
	{"pipefail negated by unsetopt is not relied on", "unsetopt NO_PIPE_FAIL; wb pr create | tail"},
	{"pipefail switched off in the same set", "set -o pipefail +o pipefail; wb pr create | tail"},
	{"pipefail named after the end of options", "set -- -o pipefail; wb pr create | tail"},
	{"pipefail named after a positional word", "set a -o pipefail; wb pr create | tail"},
	{"set -o with no name", "set -o; wb pr create | tail"},
	{"set +o nopipefail", "set +o nopipefail; wb pr create | tail"},
	{"conditional pipefail switch-off is not relied on", "set -o pipefail; true && set +o pipefail; wb pr create | tail"},

	// A group or a substitution form the reader did not see through.
	{"brace group with 2>&1 then a chain", "{ wb pr create; } 2>&1 | tail -1 && echo next"},
	{"brace group piped", "{ wb pr create; } | cat"},
	{"brace group of two commands piped", "{ wb pr create; echo done; } | tail"},
	{"subshell group piped", "(wb pr create) | tail"},
	{"nested group piped", "{ (wb pr create); } | tail"},
	{"group piped with pipe-ampersand", "{ wb pr create; } |& tail"},
	{"group whose pipefail is set inside, piped outside", "{ set -o pipefail; wb pr create; } | tail"},
	{"subshell whose pipefail is set inside, piped outside", "(set -o pipefail; wb pr create) | tail"},
	{"backtick substitution", "x=`wb pr create | tail -1`"},
	{"backtick substitution as an argument", "echo `wb worktree land . | tail`"},
	{"backtick substitution then more commands", "url=`wb pr create | tail -1` && echo $url"},
	{"backtick substitution with an escaped backtick", "echo `echo \\` ; wb pr create | tail`"},
	{"double-quoted dollar-paren substitution", `x="$(wb pr create | tail -1)"`},
	{"double-quoted substitution inside an argument", `echo "url: $(wb pr create | tail -1)"`},
	{"double-quoted nested parentheses", `x="$(echo $(wb pr create | tail -1))"`},
	{"double-quoted backtick substitution", "x=\"`wb pr create | tail -1`\""},
	{"double-quoted backtick after an escape", "x=\"\\$ `wb pr create | tail -1`\""},
	{"substitution inside a payload", `bash -c 'x="$(wb pr create | tail -1)"'`},
	{"backtick substitution after a subshell that closed", "(set -o pipefail); x=`wb pr create | tail`"},

	// A flag's value is not a marker.
	{"title that looks like --help", "wb pr create --title '--help' | tail"},
	{"title that looks like --dry-run", "wb pr create --title --dry-run | tail"},
	{"body that looks like -h", "wb pr create --body -h | tail"},
	{"model that looks like --help before the verb words", "wb --projects-root --help pr create | tail"},
	{"apply as a value is not apply but create still masks", "wb pr create --reason --apply | tail"},
	{"format value then a marker as a value", "wb pr create --format --help | tail"},

	// Verbs the first review found missing.
	{"remote claim", "wb remote claim t | tail"},
	{"remote release", "wb remote release t | tail"},
	{"worktree adopt --apply", "wb worktree adopt --apply | tail"},
	{"session park", "wb session park | tail"},
	{"session move", "wb session move s --to vm | tail"},
	{"agent dispatch", "wb agent dispatch --task t | tail"},
	{"deps bump", "wb deps bump --changed a@v1 | tail"},
	{"migrate --apply", "wb migrate --apply | tail"},
	{"repo init-remote", "wb repo init-remote o/r | tail"},
	{"branch quarantine --apply", "wb branch quarantine --apply | tail"},
	{"fleet merge-policy --apply", "wb fleet merge-policy --org o --apply | tail"},
	{"sync", "wb sync | tail"},
	{"hooks install", "wb hooks install | tail"},
	{"hooks repair", "wb hooks repair | tail"},
	{"self-update", "wb self-update | tail"},
}

// maskedPipelineRound2Holes are the shapes the second review of wb#816 found
// allowed although they still hide a verb's status, with the real nested
// substitutions the quoted-heredoc fix must keep reading.
var maskedPipelineRound2Holes = []struct {
	name    string
	command string
}{
	// A real substitution that runs a piped verb is still read as a command.
	{"quoted substitution running a piped verb", `echo "$(wb pr create | tail)"`},
	{"nested quoted substitutions", `x="$(echo "$(wb pr create | tail)")"`},
	{"command after a heredoc in a quoted substitution", "echo \"$(cat <<'EOF'\n1) prose\nEOF\nwb pr create | tail)\""},
	{"command after a heredoc with two operators", "echo \"$(cat <<'A' <<-'B'\n1) a\nA\n2) b\nB\nwb pr create | tail)\""},
	{"a backtick substitution beside the quoted one", "echo \"$(cat <<'EOF'\nprose\nEOF\n)\" `wb pr create | tail`"},
	{"a quoted substitution after a heredoc one", "x=\"$(cat <<'EOF'\nprose `wb sync | tail`\nEOF\n)\"; y=\"$(wb pr create | tail)\""},

	// A piped compound command pipes everything inside it.
	{"for loop piped", "for t in a b; do wb pr land o/r#$t; done 2>&1 | tail -5"},
	{"for loop piped with a here-string redirect", "for t in a b; do wb pr land o/r#$t; done <<< x | tail"},
	{"if piped", "if true; then wb pr create; fi | tail"},
	{"if else piped", "if true; then echo a; else wb pr create; fi | tail"},
	{"while with an input redirect piped", "while read r; do wb pr land \"$r\"; done < list | tail"},
	{"until piped", "until false; do wb pr land o/r#1; done | tail"},
	{"case piped", "case $x in a) wb pr land o/r#1;; esac | tail"},
	{"select piped", "select t in a b; do wb pr land o/r#$t; done | tail"},
	{"nested compound piped on the outside", "for a in b; do if x; then wb pr create; fi; done | tail"},
	{"compound after a leader", "if x; then for a in b; do wb pr land o/r#$a; done | tail; fi"},
	{"a wrapper shell inside a piped loop", "for t in a; do bash -c 'wb pr land o/r#1'; done | tail"},
	{"pipefail set only inside the loop does not reach the pipe", "for t in a; do set -o pipefail; wb pr land o/r#1; done | tail"},
	{"loop on several lines", "for t in a b\ndo\n  wb pr land o/r#$t\ndone | tail -1"},

	// A piped wrapper shell hides the status of whatever it runs last.
	{"bash -c piped", "bash -c 'cd x && wb pr create' | tail"},
	{"zsh -lc piped with 2>&1", "zsh -lc 'wb pr create' 2>&1 | tail -1"},
	{"bash -c piped with its own pipefail", "bash -o pipefail -c 'wb pr create' | tail"},
	{"bash -c piped with a payload that ends in the verb and --quiet", "bash -c 'wb pr create --quiet' | tail"},
	{"sh -c in a piped group", "{ sh -c 'wb land .'; } | tail"},
	{"nested wrapper shells piped", `sh -c "bash -c 'wb land .'" | tail`},
	{"wrapper with a quoted substitution payload piped", `bash -c 'x="$(wb pr create --quiet)"' | tail`},
	{"env wrapper shell piped", "env X=1 bash -c 'wb pr create' | tail"},
	{"wrapper piped under a pipefail set in a subshell that closed", "(set -o pipefail); bash -c 'wb pr create' | tail"},

	// --resume and --verify are boolean on most watched verbs.
	{"migrate --resume --apply", "wb migrate --resume --apply | tail"},
	{"migrate --apply --resume", "wb migrate --apply --resume | tail"},
	{"fleet merge-policy --resume --apply", "wb fleet merge-policy --resume --apply | tail"},
	{"stream sync --verify", "wb stream sync --verify s | tail"},
	{"worktree create --resume", "wb worktree create --resume t o/r | tail"},
	{"session move --resume takes a value", "wb session move --resume --help | tail"},
	{"migrate --verify takes a value", "wb migrate --verify --check --apply | tail"},

	// pipefail is relied on only when it is certain.
	{"switch-on in an if body", "if false; then set -o pipefail; fi; wb pr create | tail"},
	{"switch-on in an if body on its own line", "if false; then\n  set -o pipefail\nfi\nwb pr create | tail"},
	{"switch-on in a for body", "for x in; do echo; set -o pipefail; done; wb pr create | tail"},
	{"switch-on in a while body", "while false; do set -o pipefail; done; wb pr create | tail"},
	{"switch-on in a case arm", "case a in b) set -o pipefail;; esac; wb pr create | tail"},
	{"switch-on in an else body", "if true; then :; else set -o pipefail; fi; wb pr create | tail"},
	{"switch-on in a function body", "pf() { set -o pipefail; }; wb pr create | tail"},
	{"switch-on in a function body with the keyword", "function pf { set -o pipefail; }; wb pr create | tail"},
	{"switch-on in a function body with keyword and parentheses", "function pf() { set -o pipefail; }; wb pr create | tail"},
	{"switch-on in a function body on later lines", "pf()\n{\n  set -o pipefail\n}\nwb pr create | tail"},
	{"switch-on backgrounded", "set -o pipefail & wb pr create | tail"},
	{"switch-on backgrounded on its own line", "set -o pipefail &\nwb pr create | tail"},
	{"switch-on in a backgrounded group", "{ set -o pipefail; } & wb pr create | tail"},
	{"switch-on after a line ending in &&", "false &&\nset -o pipefail\nwb pr create | tail"},
	{"switch-on after a line ending in ||", "true ||\nset -o pipefail\nwb pr create | tail"},
	{"switch-on after && and a blank line", "false &&\n\nset -o pipefail\nwb pr create | tail"},
	{"switch-on behind && opening a group", "false &&\n{ set -o pipefail; }; wb pr create | tail"},
	{"switch-off in an if body", "set -o pipefail; if true; then set +o pipefail; fi; wb pr create | tail"},
	{"switch-off behind command", "set -o pipefail; command set +o pipefail; wb pr create | tail"},
	{"switch-off behind builtin", "set -o pipefail; builtin set +o pipefail; wb pr create | tail"},
	{"switch-off behind an assignment", "set -o pipefail; X=1 set +o pipefail; wb pr create | tail"},
	{"switch-off behind time", "set -o pipefail; time set +o pipefail; wb pr create | tail"},
	{"switch-off behind a bang", "set -o pipefail; ! set +o pipefail; wb pr create | tail"},
	{"switch-off in eval", "set -o pipefail; eval 'set +o pipefail'; wb pr create | tail"},
	{"switch-off by shopt", "set -o pipefail; shopt -u -o pipefail; wb pr create | tail"},
	{"switch-off by emulate sh", "set -o pipefail; emulate sh; wb pr create | tail"},
	{"switch-off by emulate -R zsh", "set -o pipefail; emulate -R zsh; wb pr create | tail"},
	{"switch-off by a setopt in another case", "set -o pipefail; setopt NoPipeFail; wb pr create | tail"},
	{"pipefail in the wrong case is not a switch-on", "set -o PIPEFAIL; wb pr create | tail"},
	{"pipefail in zsh case under set is not a switch-on", "set -o PIPE_FAIL; wb pr create | tail"},
	{"a mention in an echo", "set -o pipefail; echo pipefail; wb pr create | tail"},
	{"a mention in a grep", "set -o pipefail; grep -n pipefail script.sh; wb pr create | tail"},
	{"a conditional switch-on after a switch-on", "set -o pipefail; true && set -o pipefail; wb pr create | tail"},
	{"switch-on with the option name in the wrong case later", "set -o pipefail -o PIPEFAIL; wb pr create | tail"},
	{"a mention in a wrapper shell with no payload", "set -o pipefail; bash -o pipefail script.sh; wb pr create | tail"},
}

// maskedPipelineRound2Allowed are shapes that hide no verb's status that the
// second review of wb#816 found refused, or that the fixes must keep allowing.
var maskedPipelineRound2Allowed = []struct {
	name    string
	command string
}{
	// Prose in a quoted heredoc inside a quoted substitution is text.
	{"git commit message quoting a piped verb in backticks", "git commit -m \"$(cat <<'EOF'\nfix: the shape `wb pr create | tail -1` is refused\n\nCo-Authored-By: x\nEOF\n)\""},
	{"git commit message quoting a nested substitution", "git commit -m \"$(cat <<'EOF'\nfix: refuse $(wb pr create | tail) text\nEOF\n)\""},
	{"gh pr comment body quoting a piped verb", "gh pr comment 816 --body \"$(cat <<'EOF'\nMajor: `wb worktree create t o/r 2>&1 | tail -1 && cd /x && wb pr create` passes.\nEOF\n)\""},
	{"gh pr comment with a repo flag", "gh pr comment 816 --repo sneat-dev/wb --body \"$(cat <<'EOF'\nMajor: `wb pr create | tail`\nEOF\n)\""},
	{"gh pr create body", "gh pr create --title x --body \"$(cat <<'EOF'\n## Summary\n- refuses `wb land . | tail`\nEOF\n)\""},
	{"wb pr create body", "wb pr create --title x --body \"$(cat <<'EOF'\nRefuses `wb pr land o/r#1 | tail -1`.\nEOF\n)\""},
	{"wb pr create body then --quiet", "wb pr create --title x --body \"$(cat <<'EOF'\nRefuses `wb pr land o/r#1 | tail -1`.\nEOF\n)\" --quiet"},
	{"prose with parentheses and a plain pipe", "gh issue create --title x --body \"$(cat <<'EOF'\n1) wb pr create | tail\n2) (wb pr land | tail)\nEOF\n)\""},
	{"message with several backtick pairs", "git commit -m \"$(cat <<'EOF'\nfix: refuse wb pr create | tail\n\nThe shape `wb pr create | tail -1 && wb pr land` and `wb sync | tail` are refused.\nEOF\n)\""},
	{"echo of a heredoc in a substitution", "echo \"$(cat <<'EOF'\n`wb sync | tail`\nEOF\n)\""},
	{"an unquoted delimiter", "git commit -m \"$(cat <<EOF\nfix: the shape (wb pr create | tail) is refused\nEOF\n)\""},
	{"a dash heredoc", "git commit -m \"$(cat <<-'EOF'\n\tfix: `wb pr create | tail`\n\tEOF\n)\""},
	{"two heredocs", "echo \"$(cat <<'A' <<'B'\n1) `wb sync | tail`\nA\n2) `wb sync | tail`\nB\n)\""},
	{"a here-string beside a heredoc", "echo \"$(cat <<< x <<'EOF'\n1) `wb sync | tail`\nEOF\n)\""},
	{"a backtick after an early closing quote", "git commit -m \"$(cat <<'EOF'\nthe \"foo flag: `wb pr create | tail -1` bar\nEOF\n)\""},
	{"a heredoc with a delimiter at the very end", "echo \"$(cat <<'EOF'\n1) `wb sync | tail`\nEOF"},
	{"a heredoc whose delimiter is missing", "echo \"$(cat <<'EOF'\n1) `wb sync | tail`"},
	{"a heredoc with no delimiter word", "echo \"$(cat <<\n)\""},
	{"a double-quoted backtick substitution holding backticks as prose", "echo \"`echo \\`wb pr create | tail\\``\""},

	// A loop, a conditional or a wrapper shell nobody pipes.
	{"loop with --quiet and no pipe", "for t in a b; do wb pr land o/r#$t --quiet; done"},
	{"loop whose own output is piped holds no watched verb", "for t in a b; do echo $t; done | tail"},
	{"read verbs in a piped loop", "for t in a b; do wb worktree list; done | tail"},
	{"if with a read verb piped", "if true; then wb worktree list; fi | tail"},
	{"case with no verb piped", "case $x in a) echo a;; esac | tail"},
	{"piped loop under pipefail", "set -o pipefail; for t in a; do wb pr land o/r#$t; done | tail"},
	{"piped if under pipefail", "set -euo pipefail; if true; then wb pr create; fi | tail"},
	{"wrapper under pipefail piped", "set -o pipefail; bash -c 'wb pr create' | tail"},
	{"wrapper with a read verb piped", "bash -c 'wb worktree list' | tail"},
	{"wrapper with no verb piped", "bash -c 'echo hi' | tail"},
	{"wrapper not piped", "bash -c 'wb pr create --quiet'"},
	{"loop closed before the pipe", "for t in a; do wb pr land o/r#$t --quiet; done; echo x | tail"},
	{"a stray closer", "done | tail"},
	{"a compound word as an argument", "echo for | tail"},

	// --resume and --verify.
	{"deps bump --resume --dry-run", "wb deps bump --resume --dry-run | tail"},
	{"migrate --verify takes a value so --apply is not seen", "wb migrate --verify --apply | tail"},
	{"session move --resume takes the session", "wb session move --resume sess --dry-run | tail"},

	// Read-only spellings.
	{"sync -n", "wb sync -n | tail"},
	{"sync -n after other flags", "wb sync -o sneat-co -n | tail"},
	{"self-update --check", "wb self-update --check | tail"},
	{"worktree alias with a scoped flag", "wb wt end --help | tail"},

	// pipefail that is certain.
	{"set -euo pipefail", "set -euo pipefail; wb pr create | tail"},
	{"set -eo pipefail", "set -eo pipefail; wb pr create | tail"},
	{"set -o errexit -o pipefail", "set -o errexit -o pipefail; wb pr create | tail"},
	{"setopt pipefail", "setopt pipefail; wb pr create | tail"},
	{"set -o pipefail && verb", "set -o pipefail && wb pr create | tail"},
	{"bash -o pipefail -c", "bash -o pipefail -c 'wb pr create | tail'"},
	{"bash -c with set inside", "bash -c 'set -o pipefail; wb pr create | tail'"},
	{"first line: set -euo pipefail", "set -euo pipefail\nwb pr create | tail"},
	{"first line: set -eo pipefail", "set -eo pipefail\nwb pr create | tail"},
	{"first line: set -o errexit -o pipefail", "set -o errexit -o pipefail\nwb pr create | tail"},
	{"first line: setopt pipefail", "setopt pipefail\nwb pr create | tail"},
	{"first line: set -o pipefail &&", "set -o pipefail && wb pr create | tail\necho done"},
	{"first line: bash -o pipefail -c", "bash -o pipefail -c 'wb pr create | tail'\necho done"},
	{"first line: bash -c with set inside", "bash -c 'set -o pipefail; wb pr create | tail'\necho done"},
	{"a parent's pipefail survives a child shell that names it", "set -o pipefail; bash -o pipefail -c true; wb pr create | tail"},
	{"a plain pipefail after a function definition", "pf() { echo; }; set -o pipefail; wb pr create | tail"},
	{"a plain switch-on after a brace group", "{ echo; }; set -o pipefail; wb pr create | tail"},
}

func TestBashRefusesAPipedStateChangingWBVerb(t *testing.T) {
	t.Parallel()
	commands := []struct {
		name    string
		command string
	}{
		{"the 2026-10-02 incident shape", maskedPipelineCommand},
		{"pr create into tail", "wb pr create | tail -1"},
		{"pr land with 2>&1", "wb pr land sneat-dev/wb#5 2>&1 | tail -3"},
		{"pipe-ampersand", "wb worktree land . |& tail"},
		{"wb invoked through an absolute path", "/opt/homebrew/bin/wb land . | grep landed"},
		{"wb invoked through a relative path", "./bin/wb worktree create t o/r --original-prompt-file p | tail"},
		{"environment prefix", "WB_QUIET=0 wb worktree cleanup t --apply | tail"},
		{"env wrapper", "env FOO=1 wb worktree merge a --route auto | head"},
		{"time wrapper", "time wb pr land o/r#1 | tail"},
		{"subshell", "(wb pr create | tail -1)"},
		{"command substitution", `url=$(wb worktree create t o/r --original-prompt-file p | tail -1)`},
		{"bash -c payload", `bash -c 'wb pr create | tail'`},
		{"nested shell payload", `sh -c "bash -c 'wb land . | tail'"`},
		{"root flags before the verb", "wb --projects-root /tmp/p --non-interactive pr land o/r#1 | tail"},
		{"worktree alias wt", "wb wt create t o/r --original-prompt-file p | tail"},
		{"worktree alias worktrees", "wb worktrees land . | tail"},
		{"root create alias", "wb create t o/r --original-prompt-file p | tail"},
		{"merge leaf", "wb worktree merge prepare a | tail"},
		{"pipe in the middle of a pipeline", "printf x | wb pr create | tail"},
		{"pipe then more pipes", "wb pr create | grep a | tail"},
		{"semicolon instead of &&", "wb pr create | tail; echo done"},
		{"loop body", "for t in a b; do wb worktree cleanup $t --apply | tail -1; done"},
		{"if condition", "if wb pr land o/r#1 | tail; then echo ok; fi"},
		{"tee", "wb pr create | tee out.txt"},
		{"continuation after the pipe", "wb pr create |\n  tail -1"},
		{"line continuation before the pipe", "wb pr create \\\n  | tail -1"},
		{"pipefail set only after the pipeline", "wb pr create | tail; set -o pipefail"},
		{"pipefail switched off again", "set -o pipefail; set +o pipefail; wb pr create | tail"},
		{"zsh pipefail switched off", "setopt pipefail; unsetopt pipefail; wb pr create | tail"},
		{"set without pipefail", "set -e; wb pr create | tail"},
		{"set -o with another option", "set -o errexit; wb pr create | tail"},
		{"set with only positional words", "set -- a pipefail; wb pr create | tail"},
		{"set with a bare word", "set a pipefail; wb pr create | tail"},
		{"setopt without pipefail", "setopt extendedglob; wb pr create | tail"},
		{"pr update", "wb pr update o/r#1 | tail"},
		{"worktree end", "wb worktree end t | tail"},
		{"stream start", "wb stream start s | tail"},
		{"destructive maintenance verb with --apply", "wb worktree gc --apply | tail"},
		{"apply=true spelling", "wb worktree gc --apply=true | tail"},
		{"branch cleanup with --apply", "wb branch cleanup --apply | tail"},
	}
	commands = append(commands, maskedPipelineHoles...)
	commands = append(commands, maskedPipelineRound2Holes...)
	for _, testCase := range commands {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := inspectPipeline(t, testCase.command)
			if !decision.Deny {
				t.Fatalf("Inspect(%q) allowed a state-changing wb verb whose exit status the pipe hides", testCase.command)
			}
		})
	}
}

func TestBashAllowsEveryShapeThatDoesNotHideAVerbStatus(t *testing.T) {
	t.Parallel()
	commands := []struct {
		name    string
		command string
	}{
		{"read-only list", "wb worktree list | tail"},
		{"read-only status", "wb status | grep dirty"},
		{"commands catalog", "wb commands --search finish | head"},
		{"worktree summary", "wb worktree summary | head"},
		{"worktree info as json", "wb worktree info . --format json | jq .branch"},
		{"dry-run cleanup", "wb worktree cleanup t | tail"},
		{"dry-run cleanup of every merged task", "wb worktree cleanup --all-merged | grep landed"},
		{"dry-run gc", "wb worktree gc | tail"},
		{"explicit apply=false", "wb worktree gc --apply=false | tail"},
		{"branch cleanup dry run", "wb branch cleanup | tail"},
		{"help of a state-changing verb", "wb pr create --help | head"},
		{"short help", "wb worktree merge -h | head"},
		{"help with 2>&1", "wb pr land --help 2>&1 | head -20"},
		{"version", "wb version | head -1"},
		{"hooks check", "wb hooks check | tail"},
		{"verb ends the pipeline: prompt on stdin", "printf '%s\\n' 'the exact task request' | wb worktree create t o/r --original-prompt-file -"},
		{"verb ends the pipeline inside a chain", "cat p | wb worktree create t o/r --original-prompt-file - && cd /tmp/wt"},
		{"verb ends a multi-stage pipeline", "cat p | tr a b | wb worktree create t o/r --original-prompt-file -"},
		{"pipefail first", "set -o pipefail; wb pr create | tail -1"},
		{"pipefail in a && chain", "set -o pipefail && wb pr create | tail -1"},
		{"errexit and pipefail cluster", "set -euo pipefail; wb pr create | tail -1"},
		{"errexit cluster before the option name", "set -eo pipefail; wb worktree land . | tail"},
		{"zsh setopt", "setopt pipefail; wb pr create | tail -1"},
		{"zsh setopt in another spelling", "setopt PIPE_FAIL; wb pr create | tail -1"},
		{"bash -o pipefail -c", `bash -o pipefail -c 'wb pr create | tail'`},
		{"bash -eo pipefail -c", `bash -eo pipefail -c 'wb pr create | tail'`},
		{"set inside the payload", `bash -c 'set -o pipefail; wb pr create | tail'`},
		{"subshell with its own pipefail", "(set -o pipefail; wb pr create | tail -1)"},
		{"pipefail first and a later chain", "set -o pipefail; wb pr create | tail -1 && cd /x && wb pr land"},
		{"pipefail on a line of its own", "set -o pipefail\nwb pr create | tail -1"},
		{"pipefail in a brace group", "{ set -o pipefail; wb pr create | tail -1; }"},
		{"pipefail set in a brace group stays on", "{ set -o pipefail; }; wb pr create | tail"},
		{"pipefail in a group that is piped", "set -o pipefail; { wb pr create; } 2>&1 | tail -1 && echo next"},
		{"pipefail around a piped subshell", "set -o pipefail; (wb pr create) | tail"},
		{"pipefail around a backtick substitution", "set -o pipefail; x=`wb pr create | tail -1`"},
		{"pipefail around a double-quoted substitution", `set -o pipefail; x="$(wb pr create | tail -1)"`},
		{"pipefail around a substitution as an argument", "set -o pipefail; echo `wb pr create | tail -1`"},
		{"pipefail around an unquoted substitution", "set -o pipefail; x=$(wb pr create | tail -1)"},
		{"pipefail restored after a subshell that switched it off", "set -o pipefail; (set +o pipefail); wb pr create | tail"},
		{"pipefail in the payload's own options before another option", `bash -e -o pipefail -c 'wb pr create | tail'`},
		{"payload with its own pipefail under a parent that has it", `set -o pipefail; bash -o pipefail -c 'wb pr create | tail'`},
		{"zsh payload with its own option", `zsh -o pipefail -c 'wb pr create | tail'`},
		{"payload body sets pipefail", `sh -c "set -o pipefail; wb pr create | tail"`},
		{"payload ends the options with a double dash", `bash -o pipefail -c -- 'wb pr create | tail'`},
		{"setopt in a payload", `zsh -c 'setopt pipefail; wb pr create | tail'`},
		{"setopt then an unrelated option", "setopt pipefail extendedglob; wb pr create | tail"},
		{"set +o of another option keeps pipefail", "set -o pipefail; set +o errexit; wb pr create | tail"},
		{"bare redirect", "wb pr create > out.txt 2>&1"},
		{"status checked with &&", "wb pr create 2>&1 && echo ok"},
		{"or fallback", "wb pr create || echo failed"},
		{"pipe inside double quotes", `echo "wb pr create | tail"`},
		{"pipe inside single quotes", `grep 'wb pr create | tail' notes.md`},
		{"pipe inside a commit message", `git commit -m "docs: wb pr create | tail masks the status"`},
		{"pipe inside a heredoc", "cat <<'EOF'\nwb pr create | tail\nEOF"},
		{"comment", "echo hi # wb pr create | tail"},
		{"go test", "go test ./... | tail"},
		{"gh", "gh pr list | head"},
		{"git", "git log --oneline | head -5"},
		{"specscore", "specscore spec lint | tail"},
		{"governed wb run of another tool", "wb run -- go test ./... | tail"},
		{"a program merely named like a verb", "echo create | tail"},
		{"a word that contains wb", "web pr create | tail"},
		{"wb as an argument", "grep -rn wb pr create | tail"},
		{"another program with the same subcommand words", "git worktree create | tail"},

		// Correct allows the first review listed, so the fix does not over-deny.
		{"read verb into grep", "wb worktree list | grep a"},
		{"stdin feed ends the pipeline", "gh pr view 1 --json body -q .body | wb pr create --body-file -"},
		{"process substitution keeps the verb's own status", "wb pr create > >(tail -1)"},
		{"capture then parse", `x=$(wb pr create --format json) && jq -r .url <<<"$x"`},
		{"json to a file then parse", "wb pr create --format json > out.json && jq -r .url out.json"},
		{"json capture with a fallback", `x="$(wb pr create --format json)" || exit 1`},
		{"dollar-paren with no pipe inside", "url=$(wb pr create --quiet)"},
		{"read verb in a double-quoted substitution", `x="$(wb worktree list | tail -1)"`},
		{"read verb in a backtick substitution", "x=`wb worktree list | tail -1`"},
		{"a quoted pipe in a double-quoted substitution", `x="$(wb pr create --title 'a | b')"`},
		{"a backtick in single quotes is literal", `echo 'a ` + "`" + `wb pr create | tail` + "`" + `'`},
		{"an unterminated backtick", "echo `wb pr create"},
		{"the verb is followed by && and the pipe belongs to the next command", "wb pr create && echo done | tail"},
		{"a pipe after a heredoc line", "wb pr create --body-file - <<EOF\nbody | x\nEOF"},
		{"group not piped", "{ wb pr create; } 2>&1"},
		{"group followed by a chain", "{ wb pr create; } && echo next | tail"},
		{"group closed then a word then a pipe", "{ wb pr create; } ; echo x | tail"},
		{"a substitution closed then piped", "echo $(wb pr create) | tail"},
		{"a case pattern's closing parenthesis", "case x in a) echo hi ;; esac | tail"},
		{"unbalanced closing brace", "echo a } | tail"},
		{"a closing parenthesis inside a brace group", "{ echo a ) ; } | tail"},
		{"a dry run with the marker as its own argument", "wb pr create --title x --dry-run | tail"},
		{"help after a boolean flag", "wb pr create --draft --help | tail"},
		{"help after the verb words", "wb pr create -h | tail"},
		{"worktree adopt dry run", "wb worktree adopt | tail"},
		{"migrate dry run", "wb migrate | tail"},
		{"branch quarantine dry run", "wb branch quarantine | tail"},
		{"fleet merge-policy audit", "wb fleet merge-policy --org o | tail"},
		{"sync dry run", "wb sync --dry-run | tail"},
		{"deps bump dry run", "wb deps bump --dry-run | tail"},
		{"self-update dry run", "wb self-update --dry-run | tail"},
		{"remote claims listing", "wb remote claims | tail"},
		{"remote status", "wb remote status | tail"},
		{"session list", "wb session list | tail"},
		{"gating read verbs stay maskable", "wb ci wait --pr 5 | tail"},
		{"wb check stays maskable", "wb check | tail"},
		{"wb run stays maskable", "wb run -- go test ./... | tail"},
	}
	commands = append(commands, maskedPipelineRound2Allowed...)
	for _, testCase := range commands {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := inspectPipeline(t, testCase.command)
			if decision.Deny {
				t.Fatalf("Inspect(%q) refused a call that does not hide a verb's exit status:\n%s", testCase.command, decision.Reason)
			}
		})
	}
}

func TestMaskedPipelineRefusalExplainsTheHazardAndNamesTheQuietFix(t *testing.T) {
	t.Parallel()
	decision := inspectPipeline(t, maskedPipelineCommand)
	if !decision.Deny {
		t.Fatal("the incident command was allowed")
	}
	lines := strings.Split(strings.TrimRight(decision.Reason, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("refusal is too short to explain the hazard and the fix:\n%s", decision.Reason)
	}
	explanation := lines[0] + "\n" + lines[1]
	for _, want := range []string{"`wb worktree create`", "last command", "&&"} {
		if !strings.Contains(explanation, want) {
			t.Errorf("the two explanation lines do not mention %q:\n%s", want, explanation)
		}
	}
	for _, want := range []string{"drop the pipe", "--quiet", "pipefail"} {
		if !strings.Contains(decision.Reason, want) {
			t.Errorf("refusal does not name the fix %q:\n%s", want, decision.Reason)
		}
	}
}

func TestMaskedPipelineRefusalNeverRecommendsReadingPipeStatus(t *testing.T) {
	t.Parallel()
	for _, command := range []string{maskedPipelineCommand, "wb pr update o/r#1 | tail", "wb pr create --format json | jq .url"} {
		decision := inspectPipeline(t, command)
		if !decision.Deny {
			t.Fatalf("Inspect(%q) was allowed", command)
		}
		if lower := strings.ToLower(decision.Reason); strings.Contains(lower, "pipestatus") {
			t.Errorf("refusal for %q still recommends reading the pipe status, which an && chain has already run past:\n%s", command, decision.Reason)
		}
	}
}

func TestMaskedPipelineRefusalForAJSONConsumerRecommendsCaptureThenParse(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		"wb pr create --format json | jq -r .url",
		"wb pr create --format=json | jq -r .url",
		"wb pr update o/r#1 --format json | jq -r .url",
	} {
		decision := inspectPipeline(t, command)
		if !decision.Deny {
			t.Fatalf("Inspect(%q) was allowed", command)
		}
		for _, want := range []string{"capture", `x=$(wb pr `, "--format json) && jq", `<<<"$x"`, "pipefail"} {
			if !strings.Contains(decision.Reason, want) {
				t.Errorf("refusal for %q does not mention %q:\n%s", command, want, decision.Reason)
			}
		}
		if strings.Contains(decision.Reason, "--quiet") {
			t.Errorf("refusal for %q recommends --quiet, which does not help a json consumer:\n%s", command, decision.Reason)
		}
	}
	decision := inspectPipeline(t, "wb pr create --format table | tail")
	if !strings.Contains(decision.Reason, "--quiet") {
		t.Errorf("a non-json format lost the --quiet recommendation:\n%s", decision.Reason)
	}
}

func TestMaskedPipelineRefusalOnlyOffersQuietWhereTheVerbHasIt(t *testing.T) {
	t.Parallel()
	decision := inspectPipeline(t, "wb pr update o/r#1 | tail")
	if !decision.Deny {
		t.Fatal("pr update piped into tail was allowed")
	}
	if strings.Contains(decision.Reason, "--quiet") {
		t.Errorf("refusal offers --quiet for a verb that rejects it:\n%s", decision.Reason)
	}
	for _, want := range []string{"`wb pr update`", "pipefail"} {
		if !strings.Contains(decision.Reason, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, decision.Reason)
		}
	}
}

func TestMaskedPipelineHasNoEnvironmentOverride(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		"WB_AGENTGUARD_ALLOW_MASKED_PIPE=1 wb pr create | tail",
		"WB_AGENTGUARD_ALLOW_GH_PR_MERGE=reason wb pr create | tail",
		"wb pr create | tail # agentguard:allow",
	} {
		if decision := inspectPipeline(t, command); !decision.Deny {
			t.Errorf("Inspect(%q) was allowed by an override that must not exist", command)
		}
	}
}

func TestMaskedPipelineIsRefusedAlongsideTheOtherDenyPolicies(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	decision := Inspect(bashCall("go test ./... && wb pr create | tail", repositories.Worktree), Options{ProjectsRoot: repositories.ProjectsRoot})
	if !decision.Deny || !strings.Contains(decision.Reason, "`wb pr create`") {
		t.Fatalf("a masked pipeline behind a governed command was not refused: %+v", decision)
	}
}

func TestSplitSegmentsTreatsPipeAmpersandAndAPipeContinuationAsPipes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command string
		want    []segment
	}{
		{"pipe ampersand", "a |& b", []segment{{Words: []string{"a"}, Piped: true}, {Words: []string{"b"}, Separator: "|&"}}},
		{"plain pipe", "a | b", []segment{{Words: []string{"a"}, Piped: true}, {Words: []string{"b"}, Separator: "|"}}},
		{"newline after the pipe", "a |\n b", []segment{{Words: []string{"a"}, Piped: true}, {Words: []string{"b"}, Separator: "|"}}},
		{"newline after pipe ampersand", "a |&\n b", []segment{{Words: []string{"a"}, Piped: true}, {Words: []string{"b"}, Separator: "|&"}}},
		{"a lone ampersand still backgrounds", "a & b", []segment{{Words: []string{"a"}, Background: true}, {Words: []string{"b"}, Separator: "&"}}},
		{"a command after && is behind it", "a && b", []segment{{Words: []string{"a"}}, {Words: []string{"b"}, Separator: "&&", Behind: true}}},
		{"a command after || on the next line is behind it", "a ||\n\n b\nc", []segment{{Words: []string{"a"}}, {Words: []string{"b"}, Separator: "\n", Behind: true}, {Words: []string{"c"}, Separator: "\n"}}},
		{"a command after && and a comment is behind it", "a && # why\n b", []segment{{Words: []string{"a"}}, {Words: []string{"b"}, Separator: "\n", Behind: true}}},
		{"a command on the line after a finished one is not", "a && b\n c", []segment{{Words: []string{"a"}}, {Words: []string{"b"}, Separator: "&&", Behind: true}, {Words: []string{"c"}, Separator: "\n"}}},
		{"a newline after a heredoc line after the pipe", "a | cat <<EOF\nbody\nEOF\nb", []segment{{Words: []string{"a"}, Piped: true}, {Words: []string{"cat"}, Separator: "|"}, {Words: []string{"b"}, Separator: "\n"}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := splitSegments(testCase.command); !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("splitSegments(%q) = %#v, want %#v", testCase.command, got, testCase.want)
			}
		})
	}
}

func TestStatefulWBVerbMatchingReadsThePathPastRootFlags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		words []string
		want  string
		ok    bool
	}{
		{"plain", []string{"wb", "pr", "create"}, "pr create", true},
		{"root flag with a value", []string{"wb", "--projects-root", "/p", "pr", "create"}, "pr create", true},
		{"root flag with equals", []string{"wb", "--projects-root=/p", "--filter", "x", "pr", "land", "o/r#1"}, "pr land", true},
		{"alias", []string{"wb", "wt", "land", "."}, "worktree land", true},
		{"merge leaf matches the merge entry", []string{"wb", "worktree", "merge", "land", "r.json"}, "worktree merge", true},
		{"cleanup without apply is a dry run", []string{"wb", "worktree", "cleanup", "t"}, "", false},
		{"cleanup with apply", []string{"wb", "worktree", "cleanup", "t", "--apply"}, "worktree cleanup", true},
		{"help", []string{"wb", "pr", "land", "--help"}, "", false},
		{"dry-run", []string{"wb", "worktree", "land", "--dry-run"}, "", false},
		{"unknown verb", []string{"wb", "frobnicate"}, "", false},
		{"only the program", []string{"wb"}, "", false},
		{"only root flags", []string{"wb", "--non-interactive"}, "", false},
		{"a flag whose value is missing", []string{"wb", "--projects-root"}, "", false},
		{"positional words that merely resemble a verb", []string{"wb", "worktree", "list", "create"}, "", false},
		{"a marker as a flag's value is not a marker", []string{"wb", "pr", "create", "--title", "--help"}, "pr create", true},
		{"a dry-run marker as a flag's value", []string{"wb", "pr", "create", "--title", "--dry-run"}, "pr create", true},
		{"a marker after a flag with its value", []string{"wb", "pr", "create", "--title", "x", "--dry-run"}, "", false},
		{"a flag with an equals sign takes no next word", []string{"wb", "pr", "create", "--title=x", "--help"}, "", false},
		{"a boolean flag before a marker", []string{"wb", "pr", "create", "--draft", "--help"}, "", false},
		{"a value that looks like a verb word", []string{"wb", "--projects-root", "worktree", "pr", "create"}, "pr create", true},
		{"a value-taking flag at the very end", []string{"wb", "pr", "create", "--title"}, "pr create", true},
		{"deps bump needs no apply", []string{"wb", "deps", "bump"}, "deps bump", true},
		{"migrate needs apply", []string{"wb", "migrate"}, "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			verb, ok := statefulWBVerb(testCase.words)
			if ok != testCase.ok {
				t.Fatalf("statefulWBVerb(%v) ok = %t, want %t", testCase.words, ok, testCase.ok)
			}
			if ok && strings.Join(verb.Path, " ") != testCase.want {
				t.Fatalf("statefulWBVerb(%v) = %q, want %q", testCase.words, strings.Join(verb.Path, " "), testCase.want)
			}
		})
	}
}

func TestStatefulWBVerbReadsAJSONFormatRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		words []string
		json  bool
	}{
		{[]string{"wb", "pr", "create", "--format", "json"}, true},
		{[]string{"wb", "pr", "create", "--format=json"}, true},
		{[]string{"wb", "pr", "create", "--format", "text"}, false},
		{[]string{"wb", "pr", "create", "--format"}, false},
		{[]string{"wb", "pr", "create"}, false},
	}
	for _, testCase := range cases {
		verb, ok := statefulWBVerb(testCase.words)
		if !ok || verb.JSON != testCase.json {
			t.Errorf("statefulWBVerb(%v) = (json %t, ok %t), want json %t", testCase.words, verb.JSON, ok, testCase.json)
		}
	}
}

func TestMaskedPipelineVerbPathsAndValueFlagsAreExported(t *testing.T) {
	t.Parallel()
	if got := MaskedPipelineVerbPaths(); len(got) != len(statefulWBVerbs) {
		t.Fatalf("MaskedPipelineVerbPaths() has %d paths, want %d", len(got), len(statefulWBVerbs))
	}
	if !MaskedPipelineValueFlag(nil, "--title") || MaskedPipelineValueFlag(nil, "--dry-run") {
		t.Error("MaskedPipelineValueFlag does not tell a value-taking flag from a boolean one")
	}
	session := []string{"session", "move"}
	if !MaskedPipelineValueFlag(session, "--resume") || MaskedPipelineValueFlag([]string{"migrate"}, "--resume") ||
		MaskedPipelineValueFlag(nil, "--resume") || !MaskedPipelineValueFlag([]string{"migrate", "x"}, "--verify") {
		t.Error("MaskedPipelineValueFlag does not scope --resume and --verify to the verbs where they take a value")
	}
	if got := MaskedPipelineScopedReadOnlyFlags(); len(got) != len(scopedReadOnlyFlags) {
		t.Errorf("MaskedPipelineScopedReadOnlyFlags() has %d flags, want %d", len(got), len(scopedReadOnlyFlags))
	}
}

func TestSplitSegmentsReadsGroupsAndSubstitutionsForThePipelinePolicy(t *testing.T) {
	t.Parallel()
	segments := splitSegments("a; (b; { c; } 2>&1 | d) | e; x=$(f)")
	words := func(index int) string { return strings.Join(segments[index].Words, " ") }
	if len(segments) != 8 {
		t.Fatalf("got %d segments: %#v", len(segments), segments)
	}
	wantScopes := map[string]int{"": 1, "a": 0, "b": 1, "c": 2, "d": 1, "e": 0, "x=$": 0, "f": 1}
	for index := range segments {
		if got := len(segments[index].Scope); got != wantScopes[words(index)] {
			t.Errorf("%q is in %d groups, want %d", words(index), got, wantScopes[words(index)])
		}
	}
	subshell := segments[1].Scope[0]
	if !subshell.Subshell || !subshell.Group || !subshell.Piped {
		t.Errorf("the ( ) group is %+v, want a piped subshell group", *subshell)
	}
	brace := segments[2].Scope[1]
	if brace.Subshell || !brace.Group || !brace.Piped {
		t.Errorf("the { } group is %+v, want a piped brace group", *brace)
	}
	if !segments[3].Piped {
		t.Error("d is not marked as piped")
	}
	substitution := segments[7].Scope[0]
	if !substitution.Subshell || substitution.Group {
		t.Errorf("the $( ) substitution is %+v, want a subshell that is not a pipeable group", *substitution)
	}
}

func TestSplitSegmentsMarksGroupsBehindAConditionalAsConditional(t *testing.T) {
	t.Parallel()
	cases := []struct {
		command string
		want    bool
	}{
		{"a && { b; }", true},
		{"a || (b)", true},
		{"a | { b; }", true},
		{"a |& (b)", true},
		{"a; { b; }", false},
		{"{ a; }", false},
		{"a && { { b; }; }", true},
	}
	for _, testCase := range cases {
		segments := splitSegments(testCase.command)
		var found bool
		for _, current := range segments {
			for _, frame := range current.Scope {
				if frame.Conditional != testCase.want {
					t.Errorf("%q: a group's Conditional = %t, want %t", testCase.command, frame.Conditional, testCase.want)
				}
				found = true
			}
		}
		if !found {
			t.Errorf("%q: no group found", testCase.command)
		}
	}
}

func TestSplitSegmentsRecordsSubstitutionBodies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"backticks", "x=`a | b`", []string{"a | b"}},
		{"two backtick pairs", "echo `a` `b`", []string{"a", "b"}},
		{"an escaped backtick stays in the body", "echo `a \\` b`", []string{"a \\` b"}},
		{"an unterminated backtick", "echo `a b", []string{"a b"}},
		{"double-quoted dollar-paren", `x="$(a | b)"`, []string{"a | b"}},
		{"nested parentheses are read with the outer body", `x="$(a $(b) c)"`, []string{"a $(b) c"}},
		{"a backtick nested in a dollar-paren is read with the outer body", "x=\"$(a `b` c)\"", []string{"a `b` c"}},
		{"a dollar-paren nested in a backtick is read with the outer body", "x=\"`a $(b) c`\"", []string{"a $(b) c"}},
		{"a substitution after the first one closed", `x="$(a)$(b)"`, []string{"a", "b"}},
		{"a backtick after the quote closed", "x=\"$(a)\" `b`", []string{"a", "b"}},
		{"a heredoc body does not close the substitution", "x=\"$(cat <<'E'\n1) x `y`\nE\n)\"", []string{"cat <<'E'\n1) x `y`\nE\n"}},
		{"an escaped paren in the body", `x="$(a \) b)"`, []string{`a \) b`}},
		{"an unterminated substitution", `x="$(a b`, []string{"a b"}},
		{"double-quoted backticks", "x=\"`a | b` and `c`\"", []string{"a | b", "c"}},
		{"an unterminated backtick in double quotes", "x=\"`a b\"", []string{"a b\""}},
		{"nothing in single quotes", "echo 'a `b` $(c)'", nil},
		{"a plain double-quoted word", `echo "a b"`, nil},
		{"an unquoted dollar-paren is read as commands", "x=$(a)", nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, current := range splitSegments(testCase.command) {
				got = append(got, current.Substitutions...)
			}
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("splitSegments(%q) substitutions = %q, want %q", testCase.command, got, testCase.want)
			}
		})
	}
}

func TestPipefailSettingReadsOptionsInOrder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command string
		on      bool
		toggled bool
	}{
		{"set -o", "set -o pipefail", true, true},
		{"set +o", "set +o pipefail", false, true},
		{"cluster ending in o", "set -euo pipefail", true, true},
		{"cluster with o first", "set -oe pipefail", true, true},
		{"two options", "set -o errexit -o pipefail", true, true},
		{"off after on", "set -o pipefail +o pipefail", false, true},
		{"a bare -o", "set -o", false, false},
		{"another option", "set -o errexit", false, false},
		{"an end of options", "set -- -o pipefail", false, false},
		{"a positional word first", "set a -o pipefail", false, false},
		{"a lone dash", "set - -o pipefail", false, false},
		{"no arguments", "set", false, false},
		{"setopt", "setopt pipefail", true, true},
		{"setopt in zsh's spelling", "setopt PIPE_FAIL", true, true},
		{"setopt negated", "setopt NO_PIPE_FAIL", false, true},
		{"unsetopt", "unsetopt pipefail", false, true},
		{"unsetopt negated is not relied on", "unsetopt nopipefail", false, true},
		{"setopt another option", "setopt extendedglob", false, false},
		{"another command", "echo -o pipefail", false, false},
		{"set -o in bash's case is not pipefail", "set -o PIPEFAIL", false, false},
		{"set -o in zsh's spelling is not a setting", "set -o PIPE_FAIL", false, false},
		{"set +o negated is not a setting", "set +o nopipefail", false, false},
		{"another spelling after a setting", "set -o pipefail -o PIPEFAIL", false, false},
	}
	for _, testCase := range cases {
		words := strings.Fields(testCase.command)
		on, toggled := pipefailSetting(words[0], words)
		if on != testCase.on || toggled != testCase.toggled {
			t.Errorf("%s: pipefailSetting(%q) = (on %t, toggled %t), want (%t, %t)", testCase.name, testCase.command, on, toggled, testCase.on, testCase.toggled)
		}
	}
}

func TestShellOwnPipefailReadsOnlyTheShellsOwnOptions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		command string
		want    bool
	}{
		{"bash -c x", false},
		{"bash -o pipefail -c x", true},
		{"bash -eo pipefail -c x", true},
		{"bash -e -o pipefail -c x", true},
		{"bash -o pipefail +o pipefail -c x", false},
		{"bash +o pipefail -c x", false},
		{"bash -o errexit -c x", false},
		{"bash -o", false},
		{"bash -c x -o pipefail", false},
		{"bash --norc -o pipefail -c x", false},
		{"bash -o pipefail -- x", true},
		{"bash - x", false},
	}
	for _, testCase := range cases {
		if got := shellOwnPipefail(strings.Fields(testCase.command)); got != testCase.want {
			t.Errorf("shellOwnPipefail(%q) = %t, want %t", testCase.command, got, testCase.want)
		}
	}
}

func TestEveryStatefulWBVerbNamesAWellFormedCommandPath(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, verb := range statefulWBVerbs {
		key := strings.Join(verb.Path, " ")
		if len(verb.Path) == 0 || len(verb.Path) > maxVerbPathWords {
			t.Errorf("verb path %q has an unsupported length", key)
		}
		if seen[key] {
			t.Errorf("verb path %q is listed twice", key)
		}
		seen[key] = true
		for _, word := range verb.Path {
			if word == "" || strings.ContainsAny(word, " ") || strings.HasPrefix(word, "-") {
				t.Errorf("verb path %q holds an unusable word %q", key, word)
			}
		}
	}
}

func TestSplitSegmentsMarksFunctionBodiesAndBackgroundedGroups(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command string
		inner   string
		check   func(*scopeFrame) bool
	}{
		{"name with parentheses", "f() { x; }", "x", func(frame *scopeFrame) bool { return frame.Conditional }},
		{"function keyword", "function f { x; }", "x", func(frame *scopeFrame) bool { return frame.Conditional }},
		{"function keyword and parentheses", "function f() { x; }", "x", func(frame *scopeFrame) bool { return frame.Conditional }},
		{"a plain group is not", "{ x; }", "x", func(frame *scopeFrame) bool { return !frame.Conditional }},
		{"a group after a subshell is not", "(a); { x; }", "x", func(frame *scopeFrame) bool { return !frame.Conditional }},
		{"a function inside a function", "f() { g() { x; }; }", "x", func(frame *scopeFrame) bool { return frame.Conditional }},
		{"a group behind && on the previous line", "a &&\n{ x; }", "x", func(frame *scopeFrame) bool { return frame.Conditional }},
		{"a backgrounded group", "{ x; } & y", "x", func(frame *scopeFrame) bool { return frame.Background }},
		{"a group that is not backgrounded", "{ x; } && y", "x", func(frame *scopeFrame) bool { return !frame.Background }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			for _, current := range splitSegments(testCase.command) {
				if len(current.Words) == 1 && current.Words[0] == testCase.inner {
					last := current.Scope[len(current.Scope)-1]
					if !testCase.check(last) {
						t.Fatalf("splitSegments(%q): innermost group of %q is %+v", testCase.command, testCase.inner, *last)
					}
					return
				}
			}
			t.Fatalf("splitSegments(%q) has no command %q", testCase.command, testCase.inner)
		})
	}
}

func TestDollarParenBodyReadsAHeredocBodyAsText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rest string
		want string
	}{
		{"no heredoc", "a (b) c) d", "a (b) c"},
		{"a heredoc with a parenthesis in the body", "cat <<'E'\n1) x\nE\n) tail", "cat <<'E'\n1) x\nE\n"},
		{"a dash heredoc with an indented delimiter", "cat <<-E\n\t1) x\n\tE\n) tail", "cat <<-E\n\t1) x\n\tE\n"},
		{"a double-quoted delimiter", "cat <<\"E\"\n1) x\nE\n) tail", "cat <<\"E\"\n1) x\nE\n"},
		{"a backslash delimiter", "cat <<\\E\n1) x\nE\n) tail", "cat <<\\E\n1) x\nE\n"},
		{"a space before the delimiter", "cat << E\n1) x\nE\n) tail", "cat << E\n1) x\nE\n"},
		{"two heredocs on one line", "cat <<A <<B\n1) a\nA\n2) b\nB\n) tail", "cat <<A <<B\n1) a\nA\n2) b\nB\n"},
		{"a here-string is not a heredoc", "cat <<< x\n) tail", "cat <<< x\n"},
		{"a command after the heredoc", "cat <<E\nx\nE\nb)", "cat <<E\nx\nE\nb"},
		{"a heredoc with no delimiter word", "cat <<\n) tail", "cat <<\n"},
		{"an unterminated heredoc runs to the end", "cat <<E\n1) x\n", "cat <<E\n1) x\n"},
		{"a delimiter on the last line without a newline", "cat <<E\n1) x\nE", "cat <<E\n1) x\nE"},
		{"a less-than that is not a heredoc", "a < b)", "a < b"},
		{"a heredoc whose operator ends the text", "cat <<", "cat <<"},
	}
	for _, testCase := range cases {
		if got := dollarParenBody(testCase.rest); got != testCase.want {
			t.Errorf("%s: dollarParenBody(%q) = %q, want %q", testCase.name, testCase.rest, got, testCase.want)
		}
	}
}

func TestMentionsPipefailReadsEverySpelling(t *testing.T) {
	t.Parallel()
	for command, want := range map[string]bool{
		"set -o pipefail":        true,
		"set +o PIPEFAIL":        true,
		"setopt NO_PIPE_FAIL":    true,
		"setopt PipeFail":        true,
		"eval 'set +o pipefail'": true,
		"emulate -R zsh":         true,
		"EMULATE sh":             true,
		"echo emulated":          false,
		"set -e":                 false,
		"ls":                     false,
	} {
		if got := mentionsPipefail(strings.Fields(command)); got != want {
			t.Errorf("mentionsPipefail(%q) = %t, want %t", command, got, want)
		}
	}
}

func TestFirstWatchedVerbLooksThroughPayloadsAndStopsAtTheDepthBound(t *testing.T) {
	t.Parallel()
	if _, found := firstWatchedVerb([]string{"echo hi", "wb worktree list"}, 0); found {
		t.Error("a command line with no watched verb reported one")
	}
	verb, found := firstWatchedVerb([]string{"echo hi", "env X=1 bash -c \"sh -c 'wb pr land o/r#1'\""}, 0)
	if !found || strings.Join(verb.Path, " ") != "pr land" {
		t.Errorf("firstWatchedVerb through two shells = %v, %t, want pr land", verb.Path, found)
	}
	if verb, found := firstWatchedVerb([]string{`echo "$(wb pr create --quiet)"`}, 0); !found || strings.Join(verb.Path, " ") != "pr create" {
		t.Errorf("firstWatchedVerb in a quoted substitution = %v, %t, want pr create", verb.Path, found)
	}
	if _, found := firstWatchedVerb([]string{"wb pr create"}, maxShellUnwrapDepth+1); found {
		t.Error("firstWatchedVerb went past the depth bound")
	}
	if _, found := firstWatchedVerb([]string{"bash -c 'wb pr create'"}, 0); !found {
		t.Error("firstWatchedVerb missed a verb in a payload")
	}
	if _, found := firstWatchedVerb([]string{"FOO=1"}, 0); found {
		t.Error("an assignment on its own reported a verb")
	}
}
