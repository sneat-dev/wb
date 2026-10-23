//go:build e2e

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

const redBaseFixtureBrokenTest = "\nfunc TestBrokenAtBase(t *testing.T) { t.Fatal(\"broken at base\") }\n"

// A base branch whose own test fails can only be repaired by a pull request,
// so the ratchet has to be able to judge the pull request that repairs it.
func TestE2ECoverageChangedPassesThePullRequestThatRepairsARedBase(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource+redBaseFixtureBrokenTest)
	baseSHA := repo.commitAll("red base")
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	repo.commitAll("repair the failing test")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--affected-packages", "--target", baseSHA, "--include-e2e", "--format", "json", "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, &stdout, &stderr)
	}
	var report struct {
		RedBase *quality.RedBaseline `json:"red_base"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("%v\n%s", err, &stdout)
	}
	if report.RedBase == nil || report.RedBase.SHA != baseSHA || len(report.RedBase.FailedTests) != 1 || !strings.HasSuffix(report.RedBase.FailedTests[0], ".TestBrokenAtBase") {
		t.Fatalf("red_base = %+v, want base %s naming TestBrokenAtBase", report.RedBase, baseSHA)
	}
	for _, want := range []string{"RED merge base", baseSHA, "TestBrokenAtBase"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr = %q, want it to contain %q", &stderr, want)
		}
	}
}

func TestE2ECoverageChangedStillFailsWhenTheHeadKeepsTheBaseRed(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource+redBaseFixtureBrokenTest)
	baseSHA := repo.commitAll("red base")
	repo.writeFile("app.go", ratchetFixtureBaseSource+"\n// unrelated edit\n")
	repo.commitAll("change that leaves the test failing")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code != exitFindings || !strings.Contains(stderr.String(), "coverage could not be measured") {
		t.Fatalf("code = %d, want %d with the head measurement failure\nstdout:\n%s\nstderr:\n%s", code, exitFindings, &stdout, &stderr)
	}
}
