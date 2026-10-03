package defaultbranch

import (
	"os"
	"strings"
	"testing"
)

func TestDefaultBranchReportPersistsAndPrintsCloneFindings(t *testing.T) {
	path, err := defaultBranchReportPath(Scope{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report := Report{ReportPath: path, Desired: "main", Mode: "apply", Repositories: []Repository{{
		Repository: "acme/app", Disposition: "blocked", Error: "workflow review required",
		CanonicalClones: []Canonical{{Path: "/projects/acme/app", Disposition: "blocked", Error: "local changes present"}},
	}}}
	summarizeDefaultBranch(&report)
	if err := persistDefaultBranchReport(report); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(persisted), "workflow review required") {
		t.Fatalf("persisted report = %q err=%v", persisted, err)
	}
}
