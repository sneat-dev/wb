//go:build e2e

package orchestrate

import (
	"strings"
	"testing"
)

func TestE2EGoDependencyOwnerNativeImmutableBlobPairs(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	const sourceMod = "module example.test/app\n\ngo 1.22\n\nrequire example.test/a v1.0.0\n"
	const targetMod = "module example.test/app\n\ngo 1.22\n\nrequire example.test/a v1.1.0\n"
	const sourceSum = "example.test/a v1.0.0 h1:old\n"
	const targetSum = "example.test/a v1.1.0 h1:new\n"
	rows := []struct {
		name, sourceMod, sourceSum, targetMod, targetSum, want string
		sourceSHA, targetSHA                                   string
	}{
		{name: "valid", sourceMod: sourceMod, sourceSum: sourceSum, targetMod: targetMod, targetSum: targetSum},
		{name: "missing source module", sourceSum: sourceSum, targetMod: targetMod, targetSum: targetSum, want: "requires go.mod and go.sum"},
		{name: "missing target module", sourceMod: sourceMod, sourceSum: sourceSum, targetSum: targetSum, want: "requires go.mod and go.sum"},
		{name: "missing source sum", sourceMod: sourceMod, targetMod: targetMod, targetSum: targetSum, want: "requires go.mod and go.sum"},
		{name: "missing target sum", sourceMod: sourceMod, sourceSum: sourceSum, targetMod: targetMod, want: "requires go.mod and go.sum"},
		{name: "module parse", sourceMod: "module (", sourceSum: sourceSum, targetMod: targetMod, targetSum: targetSum, want: "parse source go.mod"},
		{name: "checksum refusal", sourceMod: sourceMod, sourceSum: sourceSum, targetMod: targetMod, targetSum: "unrelated v1 h1:x\n", want: "outside upgraded dependencies"},
	}
	// All writes finish before children read these immutable object IDs.
	for i := range rows {
		row := &rows[i]
		// A preceding target can equal the next source. This is setup only; allow-empty
		// keeps each declared revision real without changing commit-pair semantics.
		writeGoDependencyPairFiles(t, fixture.canonical, row.sourceMod, row.sourceSum)
		runEngineGit(t, fixture.canonical, "add", "-A")
		runEngineGit(t, fixture.canonical, "commit", "--allow-empty", "-m", "test: immutable dependency source "+row.name)
		row.sourceSHA = strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
		writeGoDependencyPairFiles(t, fixture.canonical, row.targetMod, row.targetSum)
		runEngineGit(t, fixture.canonical, "add", "-A")
		runEngineGit(t, fixture.canonical, "commit", "--allow-empty", "-m", "test: immutable dependency target "+row.name)
		row.targetSHA = strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			for _, blob := range []struct{ revision, path, want string }{{row.sourceSHA, "go.mod", row.sourceMod}, {row.sourceSHA, "go.sum", row.sourceSum}, {row.targetSHA, "go.mod", row.targetMod}, {row.targetSHA, "go.sum", row.targetSum}} {
				got, present := gitFileContentsAtRevision(t.Context(), fixture.canonical, blob.revision, blob.path)
				if present != (blob.want != "") || got != blob.want {
					t.Fatalf("native blob %s:%s=%q/%v want %q", blob.revision, blob.path, got, present, blob.want)
				}
			}
			count, err := proveGoDependencyUpgradePair(t.Context(), fixture.canonical, row.sourceSHA, row.targetSHA)
			if row.want != "" {
				if err == nil || !strings.Contains(err.Error(), row.want) || count != 0 {
					t.Fatalf("proof=%d/%v want zero/%q", count, err, row.want)
				}
			} else if err != nil || count != 1 {
				t.Fatalf("native proof=%d/%v want one", count, err)
			}
		})
	}
	t.Run("absent revision and path", func(t *testing.T) {
		t.Parallel()
		for _, blob := range []struct{ revision, path string }{{"missing-dependency-revision", "go.mod"}, {rows[0].sourceSHA, "missing-dependency-path"}} {
			got, present := gitFileContentsAtRevision(t.Context(), fixture.canonical, blob.revision, blob.path)
			if present || got != "" {
				t.Fatalf("absent native blob=%q/%v", got, present)
			}
		}
		if count, err := proveGoDependencyUpgradePair(t.Context(), fixture.canonical, "missing-dependency-revision", rows[0].targetSHA); count != 0 || err == nil || !strings.Contains(err.Error(), "requires go.mod and go.sum") {
			t.Fatalf("missing revision proof=%d/%v", count, err)
		}
	})
}
