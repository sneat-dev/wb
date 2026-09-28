package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/spf13/cobra"
)

func TestWaitOutputShowsAnnotationsExcerptsAndMissingRequiredChecks(t *testing.T) {
	output := waitOutput{Targets: []waitTarget{{
		Selector: "acme/app#42", Status: waitStatusSettled, State: "open", Checks: map[string]int{"passed": 2},
		Failed: []string{"unit", "lint"}, Blocked: []string{"security"}, Reason: "checks failed",
		Failures: []orchestrate.CIFailureDetail{
			{Check: "unit", Annotations: []orchestrate.CIFailureAnnotation{{Path: "cmd/app.go", StartLine: 27, Message: "wrong value"}, {Path: "cmd/other.go", Message: "missing import"}}},
			{Check: "lint", Excerpt: "first finding\nsecond finding\n"},
			{Check: "build", Reason: "compiler stopped"},
		},
	}}, ResumeArgs: []string{"wb", "wait", "pr", "acme/app#42", "--until", "checks-settled"}}
	command := &cobra.Command{}
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := printWaitOutput(command, output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"acme/app#42 settled state=open", "failed=unit,lint", "blocked=security", "(checks failed)",
		"unit: cmd/app.go:27: wrong value", "unit: cmd/other.go: missing import", "lint: first finding", "lint: second finding",
		"build: compiler stopped", `required check "security" has no passing result`, "resume: wb wait pr 'acme/app#42' --until checks-settled",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output %q does not contain %q", stdout.String(), want)
		}
	}
}
