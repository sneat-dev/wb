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
		{"bash PIPESTATUS read", "wb pr create | tail -1; test ${PIPESTATUS[0]} -eq 0"},
		{"zsh pipestatus read", "wb pr create | tail -1; test ${pipestatus[1]} -eq 0"},
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
	}
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
		{"pipe ampersand", "a |& b", []segment{{Words: []string{"a"}}, {Words: []string{"b"}, Separator: "|&"}}},
		{"plain pipe", "a | b", []segment{{Words: []string{"a"}}, {Words: []string{"b"}, Separator: "|"}}},
		{"newline after the pipe", "a |\n b", []segment{{Words: []string{"a"}}, {Words: []string{"b"}, Separator: "|"}}},
		{"newline after pipe ampersand", "a |&\n b", []segment{{Words: []string{"a"}}, {Words: []string{"b"}, Separator: "|&"}}},
		{"a lone ampersand still backgrounds", "a & b", []segment{{Words: []string{"a"}}, {Words: []string{"b"}, Separator: "&"}}},
		{"a newline after a heredoc line after the pipe", "a | cat <<EOF\nbody\nEOF\nb", []segment{{Words: []string{"a"}}, {Words: []string{"cat"}, Separator: "|"}, {Words: []string{"b"}, Separator: "\n"}}},
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
			if word == "" || strings.ContainsAny(word, " -") {
				t.Errorf("verb path %q holds an unusable word %q", key, word)
			}
		}
	}
}
