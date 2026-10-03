package main

import (
	"bytes"
	"encoding/json"
	"github.com/sneat-dev/wb/internal/archiveprune"
	"testing"
)

func TestArchiveWiringUsesParsedProjectsRoot(t *testing.T) {
	t.Parallel()
	root := newRootCmdFor(&invocation{projectsRoot: "missing-before-parse"})
	var out, stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"archive", "clean", "--projects-root", t.TempDir(), "--filter", "acme", "--format", "json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute=%v stderr=%s", err, stderr.String())
	}
	var outcome archiveprune.Outcome
	if err := json.Unmarshal(out.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.Apply || outcome.DeleteUntracked || len(outcome.Results) != 0 {
		t.Fatalf("outcome=%+v", outcome)
	}
}
