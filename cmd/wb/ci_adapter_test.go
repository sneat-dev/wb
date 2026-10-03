package main

import (
	"bytes"
	"errors"
	"github.com/sneat-dev/wb/internal/orchestrate"
	progresspkg "github.com/sneat-dev/wb/internal/progress"
	"github.com/spf13/cobra"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failAfterNWriter is shared by root wiring tests that fail a specific output write.
type failAfterNWriter struct{ writes, failAt int }

func (w *failAfterNWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errors.New("write refused")
	}
	return len(p), nil
}
func TestCIProgressRootAdaptersShareRenderer(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := newCIWaitProgress(&out, true)
	p.start("acme/app", "7", "main", "abc")
	p.report(orchestrate.PullRequestWaitProgress{Observation: 1})
	p.operationReporter("land")(progresspkg.Event{Detail: "forwarded"})
	p.update("custom")
	p.finishOperation("done")
	if !strings.Contains(out.String(), "forwarded") || !strings.Contains(out.String(), "custom") || !strings.Contains(out.String(), "done") {
		t.Fatal(out.String())
	}
	p = newCIWaitProgress(io.Discard, true)
	p.fail(io.EOF)
}
func TestCIBranchOperationAcceptsGitGrammar(t *testing.T) {
	t.Parallel()
	if err := validateCIBranch("feature/integration"); err != nil {
		t.Fatal(err)
	}
	if err := validateCIBranch("not a branch"); err == nil || !strings.Contains(err.Error(), "--target must be a valid Git branch:") {
		t.Fatalf("err=%v", err)
	}
}
func TestLiveProgressRootAdaptersShareRenderer(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := newLiveProgressWithHeartbeat(&out, true, 0)
	if !p.Enabled() {
		t.Fatal("disabled")
	}
	p.start("first")
	p.update("second")
	p.printLine("line")
	p.finish("done")
	if !strings.Contains(out.String(), "second") || !strings.Contains(out.String(), "line\n") {
		t.Fatal(out.String())
	}
	if got := shellQuoteCIWaitArg("a b"); got != "'a b'" {
		t.Fatal(got)
	}
}

func TestCIJSONRootForwardersUseSharedSelector(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	var jsonOut bool
	addJSONFormatFlags(cmd, &jsonOut)
	if outputFormatChanged(cmd) {
		t.Fatal("untouched format")
	}
	if err := cmd.ParseFlags([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
	if !outputFormatChanged(cmd) || !jsonOut {
		t.Fatal("shortcut not shared")
	}
	p := newLiveProgress(io.Discard, true)
	p.start("default")
	p.finish("done")
}

func TestCIAuditRootHonorsWriterAndTypedFindings(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := newCICmd(&invocation{})
	var out, stderr bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&stderr)
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetArgs([]string{"audit", path, "--strict"})
	err := command.Execute()
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitFindings || !strings.Contains(out.String(), "go-coverage-threshold") || stderr.Len() != 0 {
		t.Fatalf("error=%v out=%s stderr=%s", err, out.String(), stderr.String())
	}
	command = newCICmd(&invocation{})
	command.SetOut(failingWriter{err: io.ErrClosedPipe})
	command.SetErr(io.Discard)
	command.SetArgs([]string{"audit", path})
	err = command.Execute()
	if !errors.Is(err, io.ErrClosedPipe) || exitCodeFor(err, true) != exitFindings {
		t.Fatalf("writer error=%v", err)
	}
}
