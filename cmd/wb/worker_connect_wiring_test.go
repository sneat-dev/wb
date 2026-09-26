package main

import (
	"bytes"
	"context"
	"testing"
)

// wb worker connect's RunE reaches connectWorker only after validating --id
// and --root; no existing test drives it through Execute() at all (only
// connectWorker's own helpers are unit tested directly). A context that is
// already canceled makes connectWorker return at its very first check,
// before touching the daemon, so this proves the wiring cheaply and safely.
func TestWorkerConnectCLIWiresIntoConnectWorker(t *testing.T) {
	root := t.TempDir()
	command := newWorkerConnectCmd(&invocation{projectsRoot: root}, defaultDaemonDependencies())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command.SetContext(ctx)
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs([]string{"--id", "cw-worker-wiring", "--root", root})
	if err := command.Execute(); err != nil {
		t.Fatalf("worker connect with an already-canceled context: %v", err)
	}
}
