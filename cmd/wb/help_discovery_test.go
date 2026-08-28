package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRootHelpPromotesTheAgentWorktreeJourney(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	help := stdout.String()
	for _, want := range []string{
		"Start isolated work",
		"wb worktree create",
		"Inspect progress",
		"wb worktree summary",
		"Land and clean up",
		"wb worktree merge",
		"wb commands --search",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("root help does not contain %q\n%s", want, help)
		}
	}
}

func TestPrimaryWorktreeHelpContainsCopyableExamples(t *testing.T) {
	tests := []struct {
		args  []string
		wants []string
	}{
		{[]string{"worktree", "create", "--help"}, []string{
			"EXAMPLES",
			"wb worktree create improve-login owner/repository",
			"--original-prompt-file -",
			"wb worktree merge",
		}},
		{[]string{"worktree", "merge", "--help"}, []string{
			"EXAMPLES",
			"wb worktree merge . --route auto --cleanup",
			"wb worktree merge prepare",
			"wb worktree merge land",
		}},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		if code := run(test.args, &stdout, &stderr); code != exitOK {
			t.Fatalf("run(%q) exit = %d, stderr = %s", test.args, code, stderr.String())
		}
		for _, want := range test.wants {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("run(%q) help does not contain %q\n%s", test.args, want, stdout.String())
			}
		}
	}
}

func TestLeafHelpDoesNotAdvertiseRejectedPersistentFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"repo", "status", "--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "--projects-root") || strings.Contains(stdout.String(), "--filter") {
		t.Fatalf("repo status help advertises rejected root selectors:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--non-interactive") {
		t.Fatalf("repo status help omitted supported global flag:\n%s", stdout.String())
	}
}

func TestParentHelpShowsOnlySelectorsSupportedByADescendant(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"worktree", "--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "--org") {
		t.Fatalf("worktree help advertises --org although no descendant supports it:\n%s", stdout.String())
	}
	for _, want := range []string{"--projects-root", "--filter", "--non-interactive"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("worktree help omitted descendant-supported %s:\n%s", want, stdout.String())
		}
	}
}

func TestEveryRunnableHelpHidesRejectedRootSelectors(t *testing.T) {
	root := newRootCmd()
	visitPublicCommands(root, func(command *cobra.Command) {
		if !command.Runnable() {
			return
		}
		t.Run(strings.ReplaceAll(command.CommandPath(), " ", "/"), func(t *testing.T) {
			args := append(strings.Fields(strings.TrimPrefix(command.CommandPath(), "wb ")), "--help")
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != exitOK {
				t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
			}
			commandID := persistentCommandID(command)
			for flag, support := range persistentFlagSupport {
				if support[commandID] || support["*"] || command.LocalNonPersistentFlags().Lookup(flag) != nil {
					continue
				}
				if helpAdvertisesFlag(stdout.String(), flag) {
					t.Errorf("%s help advertises rejected --%s", command.CommandPath(), flag)
				}
			}
		})
	})
}

func helpAdvertisesFlag(help, name string) bool {
	want := "--" + name
	for _, line := range strings.Split(help, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == want || len(fields) > 1 && fields[1] == want {
			return true
		}
	}
	return false
}

func TestHelpResolvesAUniqueCommandNameAndUnknownTopicsFailUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"help", "merge"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("help merge exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "wb worktree merge") {
		t.Fatalf("help merge did not resolve the unique descendant:\n%s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"help", "definitely-not-a-command"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("unknown help topic exit = %d, want %d; stdout=%s stderr=%s", code, exitUsage, stdout.String(), stderr.String())
	}
}

func TestCommandSearchFindsTheFinishWorkCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"commands", "--search", "finish work", "--format", "json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	var document struct {
		SchemaVersion int `json:"schema_version"`
		Commands      []struct {
			Path string `json:"path"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode command catalog: %v\n%s", err, stdout.String())
	}
	if document.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", document.SchemaVersion)
	}
	if len(document.Commands) == 0 || document.Commands[0].Path != "wb worktree merge" {
		t.Fatalf("first command = %+v, want wb worktree merge", document.Commands)
	}
}

func TestRedirectedHelpHasNoANSIControlSequences(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "\x1b[") {
		t.Fatalf("redirected help contains ANSI escapes: %q", stdout.String())
	}
}

func TestFangHelpSupportsAdaptiveColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "\x1b[") {
		t.Fatalf("Fang color-capable help contains no ANSI styling: %q", stdout.String())
	}
}

func TestAgentNonInteractiveModeStripsFangStylingEvenWhenColorIsForced(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("WB_NON_INTERACTIVE", "1")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "\x1b[") {
		t.Fatalf("WB_NON_INTERACTIVE did not strip Fang styling: %q", stdout.String())
	}
}

func TestFangAdapterDoesNotDuplicateWBErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"definitely-not-a-command"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if count := strings.Count(stderr.String(), "unknown command"); count != 1 {
		t.Fatalf("unknown command rendered %d times, want once: %q", count, stderr.String())
	}
}

func TestUsageErrorsPointToTheNearestHelpPage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"repo", "status", "--definitely-not-a-flag"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "wb repo status --help") {
		t.Fatalf("nested usage error did not name nearest help page: %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"definitely-not-a-command"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "wb commands --search") {
		t.Fatalf("unknown command did not name intent search: %q", stderr.String())
	}
}
