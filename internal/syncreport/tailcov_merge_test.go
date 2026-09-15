package syncreport

import (
	"strings"
	"testing"
)

// TestTailCovMergeRootCollectionsCreatesTheFileWhenAbsent pins the fresh- and
// empty-file behaviour: the canonical registration is returned verbatim and
// reported as a change.
func TestTailCovMergeRootCollectionsCreatesTheFileWhenAbsent(t *testing.T) {
	for name, existing := range map[string][]byte{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			merged, changed, err := MergeRootCollections(existing)
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("changed = false, want true for a file with no registration")
			}
			if string(merged) != RootCollectionsYAML {
				t.Fatalf("merged = %q, want %q", merged, RootCollectionsYAML)
			}
		})
	}
}

// TestTailCovMergeRootCollectionsTerminatesAFileWithoutANewline proves the
// append cannot fuse the user's last line with WB's registration.
func TestTailCovMergeRootCollectionsTerminatesAFileWithoutANewline(t *testing.T) {
	merged, changed, err := MergeRootCollections([]byte("tasks: data/tasks"))
	if err != nil {
		t.Fatal(err)
	}
	want := "tasks: data/tasks\n" + RootCollectionsYAML
	if !changed || string(merged) != want {
		t.Fatalf("MergeRootCollections = (%q, %t), want (%q, true)", merged, changed, want)
	}
}

// TestTailCovMergeRootCollectionsRejectsMalformedYAML proves a root-collections
// file WB cannot understand is refused rather than overwritten, naming the file
// it could not decode.
func TestTailCovMergeRootCollectionsRejectsMalformedYAML(t *testing.T) {
	_, _, err := MergeRootCollections([]byte("sync_reports: [unclosed\n"))
	if err == nil || !strings.Contains(err.Error(), RootCollectionsPath) {
		t.Fatalf("MergeRootCollections = %v, want a decode failure naming %s", err, RootCollectionsPath)
	}
}
