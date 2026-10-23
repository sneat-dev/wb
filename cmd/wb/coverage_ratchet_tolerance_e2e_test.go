//go:build e2e

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

const toleranceFixtureSource = ratchetFixtureBaseSource + `
func Flaky(ready bool) int {
	if ready {
		return 1
	}
	return 0
}
`

const toleranceFixtureFlakyTest = "\nfunc TestFlakyReady(t *testing.T) {\n\tif Flaky(true) != 1 || Flaky(false) != 0 {\n\t\tt.Fatal(\"Flaky\")\n\t}\n}\n"

const toleranceFixtureFlakyTestWithoutTheReadyBranch = "\nfunc TestFlakyReady(t *testing.T) {\n\tif Flaky(false) != 0 {\n\t\tt.Fatal(\"Flaky\")\n\t}\n}\n"

const toleranceFixturePolicy = "version: 1\ntiming_tolerance:\n  - package: .\n    statements: 1\n    functions: [\"app.go:Flaky\"]\n    reason: the ready branch depends on timing\n"

// The policy file is the only difference between these two runs, so this
// pair fails if `wb coverage --changed` ever stops handing the loaded
// tolerances to the ratchet, or starts tolerating without a policy.
func TestE2ECoverageChangedAppliesTheHeadCheckoutsTolerancePolicy(t *testing.T) {
	t.Parallel()
	for _, withPolicy := range []bool{false, true} {
		repo := newRatchetFixtureRepo(t)
		repo.writeFile("app.go", toleranceFixtureSource)
		repo.writeFile("app_test.go", ratchetFixtureTestSource+toleranceFixtureFlakyTest)
		baseSHA := repo.commitAll("base covers the ready branch")
		repo.writeFile("app_test.go", ratchetFixtureTestSource+toleranceFixtureFlakyTestWithoutTheReadyBranch)
		if withPolicy {
			repo.writeFile(".wb/coverage-ratchet.yaml", toleranceFixturePolicy)
		}
		repo.commitAll("the ready branch is no longer reached")

		var stdout, stderr bytes.Buffer
		code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--format", "json", "--non-interactive"}, &stdout, &stderr)
		var report struct {
			Packages []quality.PackageRatchet `json:"packages"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || len(report.Packages) != 1 {
			t.Fatalf("policy=%t: %v\nstdout:\n%s\nstderr:\n%s", withPolicy, err, &stdout, &stderr)
		}
		got := report.Packages[0]
		if !withPolicy {
			if code != exitFindings || got.Tolerance != 0 || !strings.Contains(stderr.String(), "app.go:16: newly uncovered (was covered at base)") {
				t.Fatalf("no policy: code = %d, package = %+v, want the strict failure\nstderr:\n%s", code, got, &stderr)
			}
			continue
		}
		if code != 0 || got.Tolerance != 1 || len(got.Tolerated) != 1 || got.Tolerated[0].File != "app.go" || got.Tolerated[0].Function != "Flaky" {
			t.Fatalf("with policy: code = %d, package = %+v, want a pass that tolerates app.go's Flaky\nstderr:\n%s", code, got, &stderr)
		}
		if !strings.Contains(stderr.String(), "WARNING: coverage ratchet tolerance used for .") {
			t.Fatalf("stderr = %q, want the tolerance warning", &stderr)
		}
	}
}
