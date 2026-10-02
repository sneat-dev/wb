package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestWarnRedBaseNamesTheBaseCommitAndEveryFailedTest(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	warnRedBase(&stderr, nil)
	if stderr.Len() != 0 {
		t.Fatalf("a green base printed a warning: %q", stderr.String())
	}
	warnRedBase(&stderr, &quality.RedBaseline{SHA: "abc123", FailedTests: []string{"example.test/a.TestOne", "example.test/b.TestTwo"}})
	for _, want := range []string{"WARNING", "RED merge base", "2 test(s) fail at abc123", "failed at base: example.test/a.TestOne", "failed at base: example.test/b.TestTwo", "upper bound"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("warning = %q, want it to contain %q", stderr.String(), want)
		}
	}
}

func TestChangedCoverageJSONReportCarriesTheRedBase(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	report := changedCoverageReport{MergeBase: "abc123", Target: "origin/main", RedBase: &quality.RedBaseline{SHA: "abc123", FailedTests: []string{"example.test/a.TestOne"}}}
	if err := writeChangedCoverageOutputTo(&out, report, "json", ""); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		RedBase *quality.RedBaseline `json:"red_base"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RedBase == nil || decoded.RedBase.SHA != "abc123" || len(decoded.RedBase.FailedTests) != 1 {
		t.Fatalf("red_base = %+v in %s", decoded.RedBase, out.String())
	}
	out.Reset()
	if err := writeChangedCoverageOutputTo(&out, changedCoverageReport{MergeBase: "abc123"}, "json", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "red_base") {
		t.Fatalf("a green base report carries red_base: %s", out.String())
	}
}
