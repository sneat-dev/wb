package daemonhost

import (
	"bytes"
	"testing"
)

// The automatic prune's lines reach the daemon's error stream, one per line.
func TestAutoPruneLinesReachTheDaemonLog(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	log := lineLogger(&out)
	log("wb disk prune --auto: done")
	log("second")
	if out.String() != "wb disk prune --auto: done\nsecond\n" {
		t.Fatalf("log = %q", out.String())
	}
}
