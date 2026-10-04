package main

import (
	"os"
	"strings"
	"testing"
)

func TestRemotePublishHelpStatesWhatIsPublished(t *testing.T) {
	t.Parallel()
	long := remoteCommandForTest(&invocation{}, "publish").Long
	for _, want := range []string{"os, arch, cpu_count and boot_time", "never published by hand", "remote.publish.interval"} {
		if !strings.Contains(long, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}

func TestRemotePublishDefaultNotesWriterIsStderr(t *testing.T) {
	t.Parallel()
	if defaultRemoteDeps().stderr != os.Stderr {
		t.Fatal("the command's own dependencies have no stderr")
	}
}
