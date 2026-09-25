package main

import (
	"bytes"
	"io"
	"testing"
)

// TestLiveProgressUpdateAfterFinishIsANoOp drives the "progress.finished"
// early-return branch inside update: once finish has run, a later update
// must not change the rendered output.
func TestLiveProgressUpdateAfterFinishIsANoOp(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newLiveProgress(&out, true)
	progress.start("work: starting")
	progress.finish("work: done")
	afterFinishLength := out.Len()

	progress.update("work: should be ignored")
	if out.Len() != afterFinishLength {
		t.Fatalf("update after finish appended output: before=%d after=%d (%q)",
			afterFinishLength, out.Len(), out.String())
	}
}

// TestProgressOutputInteractiveReturnsTheWriterUnwrapped drives the
// "interactive" branch of progressOutput: an interactive caller gets the
// raw writer back, not the carriage-return-folding progressLineWriter.
func TestProgressOutputInteractiveReturnsTheWriterUnwrapped(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	writer := progressOutput(&out, true)
	if writer != io.Writer(&out) {
		t.Fatal("progressOutput(interactive=true) wrapped the writer instead of returning it")
	}
}
