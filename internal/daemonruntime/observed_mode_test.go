package daemonruntime

import (
	"strings"
	"testing"
)

// These cases simulate the observed mode only at the private producer pipeline.
// Every production wrapper still derives testing.Testing(), and its real refusal
// cases independently prove that a go-test process cannot launch a daemon.
func TestPrivateProcessModeRetainsIndependentExecutableSuffixFence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path              string
		mode, wantRefusal bool
	}{{"/private/wb", true, true}, {"/private/wb.test", false, true}, {"/private/wb", false, false}} {
		err := daemonRefuseTestBinaryForMode(tc.path, tc.mode)
		if (err != nil) != tc.wantRefusal || err != nil && !strings.Contains(err.Error(), "Go test binary as the WB daemon ("+tc.path+")") {
			t.Fatalf("mode %v path %q refusal=%v", tc.mode, tc.path, err)
		}
	}
}
