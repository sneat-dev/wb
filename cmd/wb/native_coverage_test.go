package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestNativeCoverageOptionsRequireFreshRunAndReachBothRunnerPaths(t *testing.T) {
	t.Parallel()
	for _, options := range []qualityOptions{{ci: true, includeE2E: true}, {resume: true, includeE2E: true}} {
		if err := validateCoverageExecutionOptions(options); err == nil || !strings.Contains(err.Error(), "--include-e2e") {
			t.Fatalf("invalid mode: %v", err)
		}
	}
	options := qualityOptions{includeE2E: true, testShards: 1, minimumCoverage: -1}
	if err := validateCoverageExecutionOptions(options); err != nil {
		t.Fatal(err)
	}
	if !runOptions(options).IncludeE2E || !coverageOptionsForCommand(options).IncludeE2E {
		t.Fatal("native tier lost in ordinary or changed runner options")
	}
	if newCoverageCmd(&invocation{}).Flags().Lookup("include-e2e").DefValue != "false" {
		t.Fatal("native tier enabled by default")
	}
}

func TestCoverageBaselinePreservesNativeTierIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/app\n\ngo 1.27\n"), 0600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "profile.cov")
	if err := os.WriteFile(profile, []byte("mode: set\nexample.test/app/app.go:2.1,2.30 1 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		out := filepath.Join(t.TempDir(), "baseline.json")
		cmd := newCoverageBaselineCmd()
		args := []string{profile, "--module", root, "--sha", "same-checkout-sha", "--out", out}
		if enabled {
			args = append(args, "--include-e2e")
		}
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		baseline, err := quality.LoadBaseline(out)
		if err != nil || baseline.IncludeE2E != enabled || baseline.SHA != "same-checkout-sha" {
			t.Fatalf("baseline: %+v %v", baseline, err)
		}
		// Matching metadata uses the artifact without touching Git or starting tests.
		baseline.SchemaVersion = 2
		if err := quality.WriteBaseline(out, baseline); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		got, err := loadOrMeasureBaseline(context.Background(), &stderr, root, "same-checkout-sha", qualityOptions{baselineFile: out, includeE2E: enabled})
		if err != nil || got.IncludeE2E != enabled || stderr.Len() != 0 {
			t.Fatalf("matching baseline: %+v %v %s", got, err, &stderr)
		}
		// Mismatched metadata must attempt fallback; the non-Git fixture makes that
		// fallback fail rather than letting an incompatible artifact pass silently.
		if _, err := loadOrMeasureBaseline(context.Background(), &stderr, root, "same-checkout-sha", qualityOptions{baselineFile: out, includeE2E: !enabled}); err == nil || !strings.Contains(stderr.String(), "include_e2e") {
			t.Fatalf("mismatch accepted: %v %s", err, &stderr)
		}
	}
}
