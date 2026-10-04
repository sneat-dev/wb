package daemonhost

import (
	"bytes"
	"strings"
	"testing"
)

func TestReportPinnedLifecycleStateIsNeverSilent(t *testing.T) {
	var out bytes.Buffer
	reportPinnedLifecycleState(&out, "", "/home/a/.wb/runtime/daemon-state.json")
	if out.String() != "" {
		t.Fatalf("an unpinned daemon reported %q", out.String())
	}
	reportPinnedLifecycleState(&out, "/old/daemon-state.json", "/home/a/.wb/runtime/daemon-state.json")
	for _, want := range []string{"/old/daemon-state.json", "/home/a/.wb/runtime/daemon-state.json"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("pinned report %q does not name %q", out.String(), want)
		}
	}
}
