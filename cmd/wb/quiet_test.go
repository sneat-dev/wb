package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/sneat-dev/wb/internal/agentguard"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// quietVerbs are the lifecycle verbs the quiet mode exists for
// (spec/features/quiet-verbs-and-masked-status-guard): the ones whose progress
// lines made a coordinator pipe them through tail, which hid a refusal.
var quietVerbs = []string{
	"create", "worktree create",
	"land", "worktree land",
	"worktree merge", "worktree merge prepare", "worktree merge land", "worktree merge resume", "worktree merge revert",
	"pr create", "pr land",
	"worktree cleanup",
}

func TestQuietIsConsumedByExactlyTheLifecycleVerbs(t *testing.T) {
	t.Parallel()
	var got []string
	for commandID, supported := range persistentFlagSupport["quiet"] {
		if supported {
			got = append(got, commandID)
		}
	}
	want := append([]string(nil), quietVerbs...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("verbs consuming --quiet = %v, want %v", got, want)
	}
}

func TestQuietIsRejectedByAVerbThatCannotUseIt(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--quiet", "worktree", "list"},
		{"--quiet", "status"},
		{"--quiet", "pr", "update", "o/r#1"},
		{"--quiet", "worktree", "abort", "t"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != exitUsage {
			t.Errorf("run(%q) exit = %d, want %d; stderr = %s", args, code, exitUsage, stderr.String())
			continue
		}
		if !strings.Contains(stderr.String(), "--quiet is not supported by") {
			t.Errorf("run(%q) stderr = %q, want the unsupported-flag refusal", args, stderr.String())
		}
	}
}

// `wb run --quiet` and `wb worktree guard --quiet` predate the root flag and
// keep their own local meaning: the local flag shadows the persistent one.
func TestQuietKeepsItsLocalMeaningOnRunAndGuard(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"run", "--quiet"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("wb run --quiet exit = %d, want usage; stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--quiet requires command mode with run --") {
		t.Fatalf("wb run --quiet reached the root flag instead of its own: %s", stderr.String())
	}
	root := newRootCmd()
	guard, _, err := root.Find([]string{"worktree", "guard"})
	if err != nil {
		t.Fatal(err)
	}
	if guard.LocalFlags().Lookup("quiet") == nil || guard.LocalNonPersistentFlags().Lookup("quiet") == nil {
		t.Fatal("worktree guard lost its own --quiet")
	}
}

func TestQuietIsDocumentedInEveryConsumersHelp(t *testing.T) {
	t.Parallel()
	for _, commandID := range quietVerbs {
		var stdout, stderr bytes.Buffer
		args := append(strings.Fields(commandID), "--help")
		if code := run(args, &stdout, &stderr); code != exitOK {
			t.Fatalf("run(%q) exit = %d, stderr = %s", args, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "--quiet") {
			t.Errorf("%s --help does not document --quiet:\n%s", commandID, stdout.String())
		}
	}
}

func TestQuietIsNotAdvertisedWhereItWouldBeRejected(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"worktree", "list", "--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "--quiet") {
		t.Errorf("worktree list --help advertises --quiet it would reject:\n%s", stdout.String())
	}
}

func TestCommandsSearchQuietFindsEveryConsumer(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"commands", "--search", "quiet", "--format", "json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	var catalog commandCatalog
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, entry := range catalog.Commands {
		found[strings.TrimPrefix(entry.Path, "wb ")] = true
	}
	for _, commandID := range quietVerbs {
		if !found[commandID] {
			t.Errorf("wb commands --search quiet does not return %q", commandID)
		}
	}
}

//nolint:paralleltest // sets a process environment variable.
func TestQuietEnvironmentIsAcceptedByVerbsThatDoNotConsumeIt(t *testing.T) {
	t.Setenv("WB_QUIET", "1")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("WB_QUIET=1 made wb version fail: exit = %d, stderr = %s", code, stderr.String())
	}
}

// The environment spelling reaches a consuming verb and only a consuming verb:
// a verb that ignores --quiet must not be silenced by a variable a caller
// exported for the whole shell.
//
//nolint:paralleltest // sets a process environment variable.
func TestQuietEnvironmentReachesOnlyTheVerbsThatConsumeIt(t *testing.T) {
	t.Setenv("WB_QUIET", "1")
	for _, test := range []struct {
		args []string
		want bool
	}{
		{[]string{"worktree", "cleanup", "--all-merged", "--projects-root", t.TempDir()}, true},
		{[]string{"worktree", "list", "--projects-root", t.TempDir()}, false},
		{[]string{"commands"}, false},
	} {
		inv := &invocation{}
		root := newRootCmdFor(inv)
		root.SetArgs(test.args)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v", test.args, err)
		}
		if inv.quiet != test.want {
			t.Errorf("WB_QUIET=1 with %v: inv.quiet = %t, want %t", test.args, inv.quiet, test.want)
		}
	}
}

func TestInventoryProgressIsSilentUnderQuietEvenWhenVerbose(t *testing.T) {
	t.Parallel()
	for _, quiet := range []bool{false, true} {
		var out bytes.Buffer
		sink := newInventoryProgress(&invocation{quiet: quiet}, &out, true)
		sink.report(worktrees.ListProgress{Index: 1, Task: "t", Path: "/p/acme/app"})
		sink.report(worktrees.ListProgress{Index: 1, Task: "t", Path: "/p/acme/app", Done: true})
		sink.finish()
		if got := out.Len() > 0; got == quiet {
			t.Errorf("quiet = %t: inventory progress wrote %q", quiet, out.String())
		}
	}
}

func TestLandingProgressIsSilentUnderQuiet(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		quiet     bool
		wantLines bool
	}{{"default prints the heartbeat lines", false, true}, {"quiet prints none", true, false}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			command := &cobra.Command{}
			command.SetErr(&stderr)
			progressSink := newLandingProgress(&invocation{quiet: test.quiet}, command, true)
			progressSink.start("acme/app", "7", "", "")
			progressSink.update("pr land: local link preflight: acme/app: started")
			progressSink.report(orchestrate.PullRequestWaitProgress{Observation: 1})
			progressSink.operationReporter("pr land")(progress.Event{Phase: "merge"})
			progressSink.finishOperation("pr land: landed")
			progressSink.fail(io.EOF)
			if got := stderr.Len() > 0; got != test.wantLines {
				t.Fatalf("stderr = %q, wantLines = %t", stderr.String(), test.wantLines)
			}
		})
	}
}

func TestWorktreeMergeProgressIsSilentUnderQuiet(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		quiet bool
	}{{"default reports", false}, {"quiet reports nothing", true}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			command := &cobra.Command{}
			command.SetErr(&stderr)
			campaign := newWorktreeMergeProgress(&invocation{quiet: test.quiet}, command, worktreeMergeFlags{progress: true})
			if reporter := campaign.reporter(); (reporter != nil) == test.quiet {
				t.Fatalf("reporter present = %t under quiet = %t", reporter != nil, test.quiet)
			}
			finishWorktreeMergeProgress(campaign, orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergeComplete}, nil)
			if test.quiet && stderr.Len() > 0 {
				t.Fatalf("quiet worktree merge wrote to stderr: %q", stderr.String())
			}
		})
	}
}

func TestRemoteClaimSuccessNotesAreDroppedUnderQuietButProblemsAreNot(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		line    string
		dropped bool
	}{
		{"remote claim: acquired task-a\n", true},
		{"remote claim: refreshed task-a\n", true},
		{"remote claim: released task-a\n", true},
		{"remote claim: task-a is held by alex@mac — proceeding without the remote claim\n", false},
		{"remote claim: took over task-a from alex@mac (stale)\n", false},
		{"remote claim skipped: no login\n", false},
		{"remote claim release skipped: held by another machine\n", false},
	} {
		var out bytes.Buffer
		command := &cobra.Command{}
		command.SetErr(&out)
		writer := outcomeClaimWriter(command, true)
		if _, err := io.WriteString(writer, test.line); err != nil {
			t.Fatal(err)
		}
		if dropped := out.Len() == 0; dropped != test.dropped {
			t.Errorf("quiet claim writer on %q: dropped = %t, want %t", test.line, dropped, test.dropped)
		}
		out.Reset()
		if _, err := io.WriteString(outcomeClaimWriter(command, false), test.line); err != nil {
			t.Fatal(err)
		}
		if out.String() != test.line {
			t.Errorf("default claim writer changed %q into %q", test.line, out.String())
		}
	}
}

// A quiet cleanup keeps its outcome lines and its warnings and drops the
// informational WB-internal lines and the claim-release success note.
//
//nolint:paralleltest // swaps the package-level cleanup engine and claim release.
func TestWorktreeCleanupQuietKeepsOutcomeAndWarningsAndDropsCommentary(t *testing.T) {
	outcome := worktrees.CleanupOutcome{
		Results: []worktrees.CleanupResult{
			{ListResult: worktrees.ListResult{Task: "fixture-task", Repository: "acme/app"}, Eligible: true, Applied: true},
		},
		Diagnostics: []worktrees.ListDiagnostic{{Task: "fixture-task", Path: "/p", Message: "unreadable manifest"}},
		Artifacts: []worktrees.LifecycleArtifact{
			{Kind: "lock", Path: "/p/lock", Disposition: "skipped", Reason: "dead"},
			{Kind: "journal", Path: "/p/journal", Disposition: "retired", Eligible: true, Applied: true, Reason: "landed"},
		},
	}
	for _, test := range []struct {
		name  string
		quiet bool
	}{{"default", false}, {"quiet", true}} {
		stubCleanupEngine(t, outcome)
		releaseRemoteClaim = func(_, task string, out io.Writer) autoReleaseResult {
			_, _ = io.WriteString(out, "remote claim: released "+task+"\n")
			return autoReleaseResult{Outcome: "released"}
		}
		var stdout, stderr bytes.Buffer
		command := newWorktreeCleanupCmd(&invocation{projectsRoot: t.TempDir(), quiet: test.quiet})
		command.SetOut(&stdout)
		command.SetErr(&stderr)
		command.SetArgs([]string{"fixture-task", "--apply", "--verbose"})
		if err := command.Execute(); err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if !strings.Contains(stdout.String(), "fixture-task") {
			t.Errorf("%s: the outcome line is missing from stdout: %q", test.name, stdout.String())
		}
		if !strings.Contains(stderr.String(), "warning: cleanup skipped malformed candidate") {
			t.Errorf("%s: the warning was dropped: %q", test.name, stderr.String())
		}
		commentary := []string{"info: cleanup WB internal lock /p/lock", "remote claim: released"}
		for _, line := range commentary {
			if got := strings.Contains(stderr.String(), line); got == test.quiet {
				t.Errorf("%s: stderr contains %q = %t, want %t:\n%s", test.name, line, got, !test.quiet, stderr.String())
			}
		}
		// The applied line is the only one saying a mutation happened, so
		// --quiet keeps it.
		if applied := "info: cleanup WB internal journal /p/journal"; !strings.Contains(stderr.String(), applied) || !strings.Contains(stderr.String(), "applied=true") {
			t.Errorf("%s: the applied=true line was dropped: %q", test.name, stderr.String())
		}
	}
}

func TestPRCreateOffersNoClosesSuggestionUnderQuiet(t *testing.T) {
	t.Parallel()
	missing := t.TempDir() + "/no-such-worktree"
	for _, quiet := range []bool{false, true} {
		inv := &invocation{projectsRoot: t.TempDir(), quiet: quiet}
		if got := suggestedClosesToPrint(inv, context.Background(), missing); got != nil {
			t.Errorf("quiet = %t: suggestedClosesToPrint = %v for an unreadable worktree, want none", quiet, got)
		}
	}
}

// The masked-pipeline policy (internal/agentguard) steps over a value-taking
// flag's value, so a word like `--help` after `--title` is read as the title
// and not as a marker. The policy cannot see cobra, so this test is what keeps
// its tables honest: every watched verb exists, and every flag of one that
// takes a value is known to the policy.
func TestEveryStatefulWBVerbIsAWBCommand(t *testing.T) {
	t.Parallel()
	root := newRootCmd()
	for _, path := range agentguard.MaskedPipelineVerbPaths() {
		command, _, err := root.Find(path)
		if err != nil || command == root || strings.TrimPrefix(command.CommandPath(), "wb ") != strings.Join(path, " ") {
			t.Errorf("the masked-pipeline policy watches %q, which is not a wb command", strings.Join(path, " "))
		}
	}
}

// watchedCommands returns every command the masked-pipeline policy watches and
// every command below one, so the leaves of `worktree merge` are read as well
// as the entry the policy lists.
func watchedCommands(t *testing.T) []*cobra.Command {
	t.Helper()
	root := newRootCmd()
	var found []*cobra.Command
	var visit func(command *cobra.Command)
	visit = func(command *cobra.Command) {
		found = append(found, command)
		for _, child := range command.Commands() {
			visit(child)
		}
	}
	for _, path := range agentguard.MaskedPipelineVerbPaths() {
		command, _, err := root.Find(path)
		if err != nil || command == root {
			continue
		}
		visit(command)
	}
	return found
}

func commandPathWords(command *cobra.Command) []string {
	return strings.Fields(strings.TrimPrefix(command.CommandPath(), "wb "))
}

// takesValue reports whether a flag needs a value word, the way the policy has
// to read it: a boolean flag, or one that stands alone when given no value,
// does not.
func takesValue(flag *pflag.Flag) bool {
	return flag.Value.Type() != "bool" && flag.NoOptDefVal == ""
}

// TestEveryValueTakingFlagOfAWatchedVerbIsKnownToTheMaskedPipelinePolicy keeps
// the policy's flag tables in step with the command tree in both directions: a
// flag that takes a value must be known as one, or the policy reads its value
// as a word of the command, and a boolean flag must not be known as one, or the
// policy steps over the word after it (`wb migrate --resume --apply` would
// lose its --apply). It reads every command below a watched verb with the
// flags that command has, local and inherited.
func TestEveryValueTakingFlagOfAWatchedVerbIsKnownToTheMaskedPipelinePolicy(t *testing.T) {
	t.Parallel()
	problems := map[string]bool{}
	for _, command := range watchedCommands(t) {
		path := commandPathWords(command)
		check := func(flag *pflag.Flag) {
			spellings := []string{"--" + flag.Name}
			if flag.Shorthand != "" {
				spellings = append(spellings, "-"+flag.Shorthand)
			}
			for _, spelling := range spellings {
				known := agentguard.MaskedPipelineValueFlag(path, spelling)
				switch {
				case takesValue(flag) && !known:
					problems["add "+spelling+" to the value flags for `wb "+strings.Join(path, " ")+"` in internal/agentguard/pipeline.go"] = true
				case !takesValue(flag) && known:
					problems["remove "+spelling+" from the value flags for `wb "+strings.Join(path, " ")+"` in internal/agentguard/pipeline.go: it is a boolean flag there"] = true
				}
			}
		}
		command.LocalFlags().VisitAll(check)
		command.InheritedFlags().VisitAll(check)
	}
	if len(problems) > 0 {
		lines := make([]string, 0, len(problems))
		for line := range problems {
			lines = append(lines, line)
		}
		sort.Strings(lines)
		t.Errorf("the masked-pipeline policy's flag tables have drifted from the command tree:\n%s", strings.Join(lines, "\n"))
	}
}

func TestWatchedCommandsIncludeTheLeavesOfWorktreeMerge(t *testing.T) {
	t.Parallel()
	leaves := 0
	for _, command := range watchedCommands(t) {
		if path := commandPathWords(command); len(path) == 3 && path[0] == "worktree" && path[1] == "merge" {
			leaves++
		}
	}
	if leaves == 0 {
		t.Error("the flag drift test does not descend into the leaves of `worktree merge`")
	}
}

func TestScopedReadOnlyFlagsAreBooleanFlagsOfTheirVerb(t *testing.T) {
	t.Parallel()
	root := newRootCmd()
	for _, scoped := range agentguard.MaskedPipelineScopedReadOnlyFlags() {
		command, _, err := root.Find(scoped.Path)
		if err != nil || command == root {
			t.Errorf("the policy reads %s as read-only on `wb %s`, which is not a wb command", scoped.Name, strings.Join(scoped.Path, " "))
			continue
		}
		var flag *pflag.Flag
		if strings.HasPrefix(scoped.Name, "--") {
			flag = command.Flags().Lookup(strings.TrimPrefix(scoped.Name, "--"))
		} else {
			flag = command.Flags().ShorthandLookup(strings.TrimPrefix(scoped.Name, "-"))
		}
		if flag == nil || takesValue(flag) {
			t.Errorf("%s is not a boolean flag of `wb %s`", scoped.Name, strings.Join(scoped.Path, " "))
		}
	}
}
