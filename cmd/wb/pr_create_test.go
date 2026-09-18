package main

import (
	"errors"
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
