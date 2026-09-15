package agents

import "os/exec"

// execLookPath is the production executable lookup, indirected so tests can
// substitute a fake ssh without a production override flag.
func execLookPath(name string) (string, error) { return exec.LookPath(name) }
