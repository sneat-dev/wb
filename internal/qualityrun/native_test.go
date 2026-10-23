package qualityrun

import (
	"bytes"
	"context"
	"github.com/sneat-dev/wb/internal/quality"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		if err := Baseline(context.Background(), BaselineRequest{Profile: profile, Module: root, SHA: "same-checkout-sha", Out: out, IncludeE2E: enabled}); err != nil {
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
		got, err := loadOrMeasureBaseline(context.Background(), &stderr, root, "same-checkout-sha", changedOptions{baselineFile: out, includeE2E: enabled})
		if err != nil || got.IncludeE2E != enabled || stderr.Len() != 0 {
			t.Fatalf("matching baseline: %+v %v %s", got, err, &stderr)
		}
		// Mismatched metadata must attempt fallback; the non-Git fixture makes that
		// fallback fail rather than letting an incompatible artifact pass silently.
		if _, err := loadOrMeasureBaseline(context.Background(), &stderr, root, "same-checkout-sha", changedOptions{baselineFile: out, includeE2E: !enabled}); err == nil || !strings.Contains(stderr.String(), "include_e2e") {
			t.Fatalf("mismatch accepted: %v %s", err, &stderr)
		}
	}
}
func TestBaselineInputFailures(t *testing.T) {
	t.Parallel()
	if err := Baseline(t.Context(), BaselineRequest{Module: t.TempDir()}); err == nil {
		t.Fatal("missing module accepted")
	}
	root := changedFixture(t)
	if err := Baseline(t.Context(), BaselineRequest{Module: root, Profile: filepath.Join(root, "missing")}); err == nil {
		t.Fatal("missing profile accepted")
	}
}
