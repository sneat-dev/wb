package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestStatusProgressRendersCompletionCounter(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newStatusProgress(&out, true)
	progress.start(2)
	progress.complete(
		qualityTarget{repository: "acme/one"},
		repositoryStatusInfo{Repository: "acme/one", Status: "clean"},
	)
	progress.complete(
		qualityTarget{repository: "acme/two"},
		repositoryStatusInfo{Repository: "acme/two", Status: "attention"},
	)
	progress.finish()

	rendered := out.String()
	for _, want := range []string{
		"status: 0/2 repositories inspected",
		"status: 1/2 repositories inspected; acme/one: clean",
		"status: 2/2 repositories inspected; acme/two: attention",
		"status: inspected 2 repositories in",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("progress output missing %q: %q", want, rendered)
		}
	}
	if !strings.HasSuffix(rendered, "\n") {
		t.Errorf("finished progress did not end its live line: %q", rendered)
	}
}

func TestStatusProgressCanBeDisabled(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newStatusProgress(&out, false)
	progress.start(1)
	progress.complete(
		qualityTarget{repository: "acme/one"},
		repositoryStatusInfo{Repository: "acme/one", Status: "clean"},
	)
	progress.finish()
	if out.Len() != 0 {
		t.Fatalf("disabled progress wrote %q", out.String())
	}
}
