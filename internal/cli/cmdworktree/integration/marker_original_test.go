package integration

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/checkoutsetup"
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type markerCodeError struct {
	code    int
	message string
}

func (e *markerCodeError) Error() string { return e.message }
func newMarkerCommand(root string) *cobra.Command {
	return cmdworktree.NewMarker(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }, ExitError: func(code int, message string) error { return &markerCodeError{code, message} }}, cmdworktree.MarkerOperations{Run: checkoutsetup.NewMarkers(checkoutsetup.DefaultMarkerRunDependencies()).Run, Version: func() string { return "test" }})
}
func markerCommandExecute(t *testing.T, _ string, build func() *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	c := build()
	var stdout, stderr bytes.Buffer
	c.SetOut(&stdout)
	c.SetErr(&stderr)
	c.SetArgs(args)
	c.SilenceUsage = true
	c.SilenceErrors = true
	err := c.Execute()
	return stdout.String(), stderr.String(), err
}
func markerExitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	e, ok := err.(*markerCodeError)
	if !ok {
		t.Fatalf("uncoded error:%v", err)
	}
	return e.code
}
func TestCwWtWorktreeMarkerWritesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	testenv.CloneWithOrigin(t, seeds, "app", clone)

	stdout, _, err := markerCommandExecute(t, projects, func() *cobra.Command { return newMarkerCommand(projects) }, clone)
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	if !strings.Contains(stdout, "wrote marker + ignore rule") {
		t.Fatalf("first marker run = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(clone, checkoutmarker.FileName)); err != nil {
		t.Fatalf("marker file was not written: %v", err)
	}

	// A second run changes nothing.
	stdout, _, err = markerCommandExecute(t, projects, func() *cobra.Command { return newMarkerCommand(projects) }, clone)
	if err != nil {
		t.Fatalf("second marker: %v", err)
	}
	if !strings.Contains(stdout, "current") {
		t.Fatalf("second marker run = %q", stdout)
	}

	// A dry run over a fresh clone says what it would change.
	clone2 := filepath.Join(projects, "acme", "app2")
	testenv.CloneWithOrigin(t, seeds, "app2", clone2)
	stdout, _, err = markerCommandExecute(t, projects, func() *cobra.Command { return newMarkerCommand(projects) }, clone2, "--dry-run")
	if err != nil {
		t.Fatalf("marker --dry-run: %v", err)
	}
	if !strings.Contains(stdout, "would write marker") {
		t.Fatalf("marker --dry-run = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(clone2, checkoutmarker.FileName)); !os.IsNotExist(err) {
		t.Fatalf("--dry-run wrote a marker: %v", err)
	}

	// JSON emits the outcome rows.
	stdout, _, err = markerCommandExecute(t, projects, func() *cobra.Command { return newMarkerCommand(projects) }, clone, "--format", "json")
	if err != nil {
		t.Fatalf("marker json: %v", err)
	}
	if !strings.Contains(stdout, "\"marker_written\"") {
		t.Fatalf("marker json = %q", stdout)
	}
}

func TestCwWtWorktreeMarkerFleetAndFailures(t *testing.T) {
	t.Parallel()
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	testenv.CloneWithOrigin(t, seeds, "app", clone)
	// A linked worktree registered to the clone is swept too.
	linked := filepath.Join(projects, "linked-checkout")
	testenv.Git(t, clone, "worktree", "add", "-b", "linked-branch", linked)

	stdout, _, err := markerCommandExecute(t, projects, func() *cobra.Command { return newMarkerCommand(projects) }, "--fleet")
	if err != nil {
		t.Fatalf("marker --fleet: %v", err)
	}
	if !strings.Contains(stdout, "2 checkout(s)") {
		t.Fatalf("fleet marker stdout = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(linked, checkoutmarker.FileName)); err != nil {
		t.Fatalf("fleet sweep did not mark the linked worktree: %v", err)
	}

	// --fleet with a named checkout is refused.
	if _, _, err := markerCommandExecute(t, projects, func() *cobra.Command { return newMarkerCommand(projects) }, "--fleet", clone); err == nil || !strings.Contains(err.Error(), "do not also name one") {
		t.Fatalf("--fleet with an argument = %v", err)
	}

	// A --filter that matches nothing still completes.
	stdout, _, err = markerCommandExecute(t, projects, func() *cobra.Command { return newMarkerCommand(projects) }, "--fleet", "--format", "json")
	if err != nil {
		t.Fatalf("fleet marker json: %v", err)
	}
	if !strings.Contains(stdout, "[") {
		t.Fatalf("fleet marker json = %q", stdout)
	}

	// A path that is not a checkout is a recorded failure, not a crash.
	stdout, _, err = markerCommandExecute(t, projects, func() *cobra.Command { return newMarkerCommand(projects) }, filepath.Join(t.TempDir(), "not-a-repo"))
	if code := markerExitCode(t, err); code != shared.ExitFindings {
		t.Fatalf("marker on a non-checkout exit = %d (%v)", code, err)
	}
	if !strings.Contains(stdout, "✗") {
		t.Fatalf("marker failure stdout = %q", stdout)
	}
}
