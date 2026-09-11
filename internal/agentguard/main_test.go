package agentguard

import (
	"fmt"
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestMain points WB_HOME at a throwaway directory for the whole package, so
// no test can ever append to the operator's real
// ~/.wb/agentguard/gh-pr-merge-overrides.jsonl, the audit log a fleet review
// reads to tell "the guard was never reached" from "the guard was deliberately
// overridden". Tests that read the log back still set their own WB_HOME with
// t.Setenv.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "agentguard-wb-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create a throwaway WB_HOME: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv(wbhome.EnvOverride, home); err != nil {
		fmt.Fprintf(os.Stderr, "point WB_HOME at %s: %v\n", home, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
