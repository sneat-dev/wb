package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPRCreateRejectsBodyAndBodyFileTogether(t *testing.T) {
	command := newPRCreateCmd()
	command.SilenceUsage = true
	command.SetArgs([]string{"--body", "x", "--body-file", "x.md"})
	err := command.Execute()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(exit.message, "--body") || !strings.Contains(exit.message, "--body-file") {
		t.Fatalf("message = %q, want it to name both flags", exit.message)
	}
}

func TestPRCreateRejectsDraftWithAutoMerge(t *testing.T) {
	command := newPRCreateCmd()
	command.SilenceUsage = true
	command.SetArgs([]string{"--draft", "--auto-merge"})
	err := command.Execute()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(exit.message, "--draft") || !strings.Contains(exit.message, "--auto-merge") {
		t.Fatalf("message = %q, want it to name both flags", exit.message)
	}
}

func TestPRCreateRejectsApprovedByWithoutAutoMerge(t *testing.T) {
	command := newPRCreateCmd()
	command.SilenceUsage = true
	command.SetArgs([]string{"--approved-by", "review.md"})
	err := command.Execute()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(exit.message, "--approved-by") {
		t.Fatalf("message = %q, want it to name --approved-by", exit.message)
	}
}

// Round 3, MAJOR fix: --review-comment/--review-comment-file are only ever
// read by --land; --auto-merge alone never posts the identity form's
// comment, so accepting either flag without --land would silently ignore
// it rather than refuse.
func TestPRCreateRejectsReviewCommentWithoutLand(t *testing.T) {
	command := newPRCreateCmd()
	command.SilenceUsage = true
	command.SetArgs([]string{"--auto-merge", "--approved-by", "opus@codex@run-1", "--review-comment", "looks good"})
	err := command.Execute()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(exit.message, "--review-comment") || !strings.Contains(exit.message, "--land") {
		t.Fatalf("message = %q, want it to name --review-comment and --land", exit.message)
	}
}

func TestPRCreateRejectsAllowUnfencedWithoutAutoMerge(t *testing.T) {
	command := newPRCreateCmd()
	command.SilenceUsage = true
	command.SetArgs([]string{"--allow-unfenced"})
	err := command.Execute()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(exit.message, "--allow-unfenced") {
		t.Fatalf("message = %q, want it to name --allow-unfenced", exit.message)
	}
}

// TestPRCreateAllowsApprovedByAndAllowUnfencedWithLand proves B1: the
// contract's own headline example, `wb pr create --land --approved-by
// <review>`, must not exit 2 before it ever reaches CreatePullRequest.
// --approved-by/--allow-unfenced were only exempted for --auto-merge, so
// --land alone (without --auto-merge) used to be rejected as a usage error.
func TestPRCreateAllowsApprovedByAndAllowUnfencedWithLand(t *testing.T) {
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	}()
	command := newPRCreateCmd()
	command.SilenceUsage = true
	command.SetArgs([]string{"--land", "--approved-by", "review.md", "--allow-unfenced"})
	err = command.Execute()
	var exit *exitError
	if errors.As(err, &exit) && exit.code == exitUsage {
		t.Fatalf("--land --approved-by --allow-unfenced must not be a usage error: %v", exit)
	}
}

func TestPRCreateRejectsAddWithCommitAll(t *testing.T) {
	command := newPRCreateCmd()
	command.SilenceUsage = true
	command.SetArgs([]string{"--add", "x.go", "--commit-all", "-m", "feat: x"})
	err := command.Execute()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(exit.message, "--add") || !strings.Contains(exit.message, "--commit-all") {
		t.Fatalf("message = %q, want it to name both flags", exit.message)
	}
}

func TestPRCreateHelpStatesItsContract(t *testing.T) {
	command := newPRCreateCmd()
	var output strings.Builder
	command.SetOut(&output)
	if err := command.Help(); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"CI is the gate", "canonical clone", "wip", "--auto-merge", "--draft --auto-merge", "--add",
	} {
		if !strings.Contains(output.String(), fragment) {
			t.Fatalf("help does not contain %q:\n%s", fragment, output.String())
		}
	}
}
