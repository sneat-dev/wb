package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // Actual private GH discovery resolves through the process PATH fixture.
func TestDepsDirectiveRootRegistersTheFamilyAndUsesNativeCurrentRoot(t *testing.T) {
	root := t.TempDir()
	inv := &invocation{projectsRoot: root, nonInteractive: true}
	family := newDepsCmd(inv)
	directive, _, err := family.Find([]string{"go-directive"})
	if err != nil || directive == family {
		t.Fatalf("directive registration=%v/%v", directive, err)
	}
	if len(directive.Commands()) != 2 {
		t.Fatalf("children=%v", directive.Commands())
	}
	check, _, err := directive.Find([]string{"check"})
	if err != nil {
		t.Fatal(err)
	}
	directive.RemoveCommand(check)
	var output bytes.Buffer
	check.SetOut(&output)
	check.SetErr(&output)
	check.SetContext(context.Background())
	check.SetArgs([]string{root})
	check.SilenceUsage = true
	check.SilenceErrors = true
	if err := check.Execute(); err != nil || !strings.Contains(output.String(), "no Go module found at or under "+root) {
		t.Fatalf("native empty module=%q/%v", output.String(), err)
	}
	// Selection observes the invocation after construction, and the real discovery
	// resolver refuses a private non-directory layout before assessment or output.
	cwCovFakeGH(t, "directive-user", nil, `[]`)
	blocking := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocking, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	inv.projectsRoot = filepath.Join(blocking, "child")
	report, _, err := directive.Find([]string{"report"})
	if err != nil {
		t.Fatal(err)
	}
	directive.RemoveCommand(report)
	output.Reset()
	report.SetOut(&output)
	report.SetErr(&output)
	report.SetContext(context.Background())
	report.SetArgs(nil)
	report.SilenceUsage = true
	report.SilenceErrors = true
	err = report.Execute()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(err.Error(), blocking) || output.Len() != 0 {
		t.Fatalf("current root refusal=%v output=%q", err, output.String())
	}
}
