//go:build e2e

package orchestrate

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestE2ESourcePROwnerExcludesNativeRecordedSourceAlreadyInTarget(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	ancestor := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(f.canonical, "ancestor-gap-target.txt"), "target advance\n")
	runEngineGit(t, f.canonical, "add", "-A")
	runEngineGit(t, f.canonical, "commit", "-m", "test: actual target descendant")
	target := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "checkout", "-b", "ancestor-gap-source")
	writeEngineFile(t, filepath.Join(f.canonical, "ancestor-gap-source.txt"), "new source\n")
	runEngineGit(t, f.canonical, "add", "-A")
	runEngineGit(t, f.canonical, "commit", "-m", "test: actual unlanded source")
	source := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "checkout", "main")
	runEngineGit(t, f.canonical, "merge", "--no-ff", "--no-edit", "ancestor-gap-source")
	candidate := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	if ancestor == target || source == candidate {
		t.Fatal("fixture must have distinct native target/source histories")
	}
	for _, pair := range []struct {
		older, newer string
		want         bool
	}{{ancestor, target, true}, {source, target, false}, {source, candidate, true}} {
		ok, err := isMergeAncestor(t.Context(), f.canonical, pair.older, pair.newer)
		if err != nil || ok != pair.want {
			t.Fatalf("native ancestry %s -> %s=%t/%v want %t", pair.older, pair.newer, ok, err, pair.want)
		}
	}
	receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "main", TargetSHA: target, Candidate: WorktreeMergeCandidate{SHA: candidate}, Sources: []WorktreeMergeSource{{SHA: ancestor, Merged: true}, {SHA: source, Merged: true}}}
	// Observation-only recorder: ordinal zero delegates every real Git query.
	// The older recorded source is discovered but excluded by native containment.
	run := &landedFailureOwnerRunner{Runner: defaultRunner, path: f.canonical, args: []string{"merge-base", ancestor, target}}
	got, err := absorbedSourceHeadsWithRunner(t.Context(), run, f.canonical, receipt, 0, 0)
	if err != nil || !reflect.DeepEqual(got, []string{source}) || run.seen != 1 {
		t.Fatalf("native heads=%v error=%v exact old-source query=%d; want only %s and one actual ancestry observation", got, err, run.seen, source)
	}
	if head := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD")); head != candidate {
		t.Fatalf("read-only discovery moved native HEAD: %s", head)
	}
}
