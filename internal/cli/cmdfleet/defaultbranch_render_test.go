package cmdfleet

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/defaultbranch"
)

func TestDefaultBranchReportPrintsCloneFindingsAndWriterErrors(t *testing.T) {
	t.Parallel()
	report := defaultbranch.Report{Desired: "main", Mode: "apply", Repositories: []defaultbranch.Repository{{Repository: "acme/app", Disposition: "blocked", Error: "workflow review required", CanonicalClones: []defaultbranch.Canonical{{Path: "/projects/acme/app", Disposition: "blocked", Error: "local changes present"}}}}}
	var output bytes.Buffer
	if err := printDefaultBranchReport(&output, report); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "acme/app: blocked") || !strings.Contains(got, "/projects/acme/app: blocked") {
		t.Fatalf("printed report = %q", got)
	}
	for failAt := 1; failAt <= 5; failAt++ {
		writer := &defaultBranchFailWriter{failAt: failAt}
		if err := printDefaultBranchReport(writer, report); err == nil {
			t.Fatalf("output failure at write %d was ignored", failAt)
		}
	}
}

func TestDefaultBranchRenderingPreservesEveryCloneWriterError(t *testing.T) {
	t.Parallel()
	report := defaultbranch.Report{Repositories: []defaultbranch.Repository{{Repository: "acme/app", Disposition: "error", Error: "remote failed", CanonicalClones: []defaultbranch.Canonical{{Path: "checkout", Disposition: "blocked", Error: "local changed"}}}}}
	successful := &defaultBranchFailWriter{failAt: -1}
	if err := printDefaultBranchReport(successful, report); err != nil || successful.writes == 0 {
		t.Fatal(err, successful.writes)
	}
	for failure := 1; failure <= successful.writes; failure++ {
		writer := &defaultBranchFailWriter{failAt: failure}
		if err := printDefaultBranchReport(writer, report); err == nil || writer.writes != failure {
			t.Fatal(failure, writer.writes, err)
		}
	}
}
