package main

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoRootFactoryKeepsUsageAndFindingsErrorIdentity(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		args    []string
		code    int
		message string
	}{
		{"receipt usage", []string{"transfer", "cleanup"}, exitUsage, "--receipt is required"},
		{"missing Git row", []string{"status", filepath.Join(t.TempDir(), "missing"), "--format=json"}, exitFindings, "see the `error` field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cmd := newRepoCmd(testInvocation(t, t.TempDir()))
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs(test.args)
			err := cmd.Execute()
			var coded *exitError
			if !errors.As(err, &coded) || coded.code != test.code || exitCodeFor(err, true) != test.code || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error=%v code=%d", err, exitCodeFor(err, true))
			}
			if test.code == exitFindings && !strings.Contains(out.String(), `"status": "error"`) {
				t.Fatalf("findings without row=%q", out.String())
			}
		})
	}
}
