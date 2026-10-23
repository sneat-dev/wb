package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

func TestStreamRootWiringUsesParsedProjectsRootAndLegacyCodedIdentity(t *testing.T) {
	projects := t.TempDir()
	store, err := streams.Open(projects)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(streams.Stream{Name: "chosen", Members: []streams.Member{{Repository: "acme/app", Role: streams.RoleConsumer, Worktree: filepath.Join(projects, "missing-worktree"), Branch: "stream/chosen", Base: "main"}}}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		code int
		want string
	}{{"findings from selected store", []string{"stream", "status", "chosen", "--format", "json", "--projects-root", projects}, exitFindings, "stream status reported findings"}, {"registered session required", []string{"stream", "start", "new", "acme/app", "--mode", "agent", "--projects-root", projects, "--format", "json"}, exitUsage, "live registered session"}} {
		t.Run(test.name, func(t *testing.T) {
			inv := testInvocation(t, t.TempDir())
			root := newRootCmdFor(inv)
			var out, errOut bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errOut)
			root.SilenceUsage = true
			root.SilenceErrors = true
			root.SetArgs(test.args)
			err := root.Execute()
			var coded *exitError
			if !errors.As(err, &coded) || coded.code != test.code || exitCodeFor(err, true) != test.code || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v stdout=%s stderr=%s", err, out.String(), errOut.String())
			}
			if test.code == exitFindings && (!strings.Contains(out.String(), `"stream": "chosen"`) || !strings.Contains(out.String(), `"outcome": "findings"`)) {
				t.Fatalf("selected store finding=%s", out.String())
			}
			if test.code == exitUsage && !strings.Contains(out.String(), `"refusal_code": "usage"`) {
				t.Fatal(out.String())
			}
		})
	}
}
