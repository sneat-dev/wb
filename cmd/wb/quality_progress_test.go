package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestQualityProgressShowsCheckAndRepositoryCompletion(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newQualityProgress(&out, true, "verify", 2)
	progress.start()
	progress.report(quality.Progress{
		Repository: "acme/one", Module: ".", Command: "go test ./...", State: quality.ProgressStarted,
	})
	progress.report(quality.Progress{
		Repository: "acme/one", Module: ".", Command: "go test ./...", State: quality.ProgressCompleted, Status: quality.StatusPassed,
	})
	progress.report(quality.Progress{
		Repository: "acme/one", State: quality.ProgressRepositoryCompleted, Status: quality.StatusPassed,
	})
	progress.finish()

	rendered := out.String()
	for _, want := range []string{
		"verify: 0/2 repositories completed",
		"acme/one . — go test ./...: started",
		"acme/one . — go test ./...: passed",
		"verify: 1/2 repositories completed; acme/one: passed",
		"verify: completed 1 repositories",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("progress output missing %q: %q", want, rendered)
		}
	}
}
