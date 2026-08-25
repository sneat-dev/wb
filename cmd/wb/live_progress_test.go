package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLiveProgressReplacesAndTerminatesLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newLiveProgress(&out, true)
	progress.start("work: starting")
	progress.update("work: one")
	progress.update("work: two")
	progress.finish("work: done")

	rendered := out.String()
	for _, want := range []string{"work: starting", "work: one (", "work: two (", "work: done ("} {
		if !strings.Contains(rendered, want) {
			t.Errorf("progress output missing %q: %q", want, rendered)
		}
	}
	if !strings.HasSuffix(rendered, "\n") {
		t.Errorf("finished progress did not end its live line: %q", rendered)
	}
}

func TestLiveProgressCanBeDisabled(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newLiveProgress(&out, false)
	progress.start("starting")
	progress.update("working")
	progress.finish("done")
	if out.Len() != 0 {
		t.Fatalf("disabled progress wrote %q", out.String())
	}
}
