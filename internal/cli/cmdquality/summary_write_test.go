package cmdquality

import (
	"errors"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
	"testing"
)

func TestWriteCoverageOutputToSummaryPropagatesAWriteFailure(t *testing.T) {
	t.Parallel()
	artifacts := qualityrun.CoverageArtifacts{Report: qualityrun.ArtifactReference{Path: "coverage.yaml", SHA256: "digest"}, Diagnostics: &qualityrun.ArtifactReference{Path: "diagnostics.yaml", SHA256: "diagnostics-digest"}}
	report := quality.CoverageReport{
		Statements: 10, Covered: 8, Percentage: 80,
		Repositories: []quality.RepositoryCoverage{{
			Repository: "acme/app", Statements: 10, Covered: 8,
			Diagnostic: &quality.CoverageDiagnostic{Manifest: "manifest.json", SHA256: "abc"},
		}},
	}

	t.Run("headline", func(t *testing.T) {
		t.Parallel()
		err := writeCoverageOutputTo(&failAtCallWriter{failAt: 1}, report, "summary", artifacts)
		if !errors.Is(err, errAtWrite) {
			t.Fatalf("writeCoverageOutputTo (headline) returned %v, want errAtWrite", err)
		}
	})
	t.Run("diagnostics", func(t *testing.T) {
		t.Parallel()
		err := writeCoverageOutputTo(&failAtCallWriter{failAt: 2}, report, "summary", artifacts)
		if !errors.Is(err, errAtWrite) {
			t.Fatalf("writeCoverageOutputTo (diagnostics) returned %v, want errAtWrite", err)
		}
	})
}

var errAtWrite = errors.New("failed write")

type failAtCallWriter struct{ failAt, calls int }

func (w *failAtCallWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return 0, errAtWrite
	}
	return len(p), nil
}
