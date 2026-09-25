package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/testenv"
)

// cwWtFailWriter accepts a bounded number of writes and then fails every later
// one with a fixed error. It is how a command's "the report could not be
// written" branches are reached, which are otherwise dead code in a test.
type cwWtFailWriter struct {
	Allow  int
	Writes int
}

var errCwWtWrite = errors.New("cwWt: injected write failure")

func (writer *cwWtFailWriter) Write(payload []byte) (int, error) {
	if writer.Writes >= writer.Allow {
		return 0, errCwWtWrite
	}
	writer.Writes++
	return len(payload), nil
}

// cwWtExecOut is cwCovExec with caller-supplied output streams, so a test can
// inject a writer that fails. It points the shared projectsRoot global at the
// fixture for the duration of the call.
func cwWtExecOut(t *testing.T, projects string, build func() *cobra.Command, stdout, stderr io.Writer, args ...string) error {
	t.Helper()
	testenv.Isolate(t)
	previousRoot := projectsRoot
	projectsRoot = projects
	t.Cleanup(func() { projectsRoot = previousRoot })

	command := build()
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetContext(context.Background())
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs(args)
	return command.Execute()
}

// cwWtRunCmd runs a command with both a stdin body and captured output.
func cwWtRunCmd(t *testing.T, projects, stdin string, build func() *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	testenv.Isolate(t)
	previousRoot := projectsRoot
	projectsRoot = projects
	t.Cleanup(func() { projectsRoot = previousRoot })

	command := build()
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetContext(context.Background())
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	if stdin != "" {
		command.SetIn(bytes.NewReader([]byte(stdin)))
	} else {
		command.SetIn(bytes.NewReader(nil))
	}
	command.SetArgs(args)
	err = command.Execute()
	return out.String(), errOut.String(), err
}

// cwWtGitRepo makes a real repository with one commit, so commands that need a
// readable Git checkout have something honest to work on.
func cwWtGitRepo(t *testing.T, path string) string {
	t.Helper()
	initTestRepository(t, path)
	runGit(t, path, "config", "user.email", "wb-cwwt@example.test")
	runGit(t, path, "config", "user.name", "WB CwWt")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("cwWt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "add", ".")
	runGit(t, path, "commit", "-m", "cwWt init")
	return path
}

// cwWtWriteFile writes a file, creating parent directories.
func cwWtWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
