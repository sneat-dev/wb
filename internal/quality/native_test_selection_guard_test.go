package quality

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeTestSelectionHasNoUndiscoveredAssertions(t *testing.T) {
	t.Parallel()
	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	problems, err := FindNativeTestSelectionProblems(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Fatalf("native test discovery problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestNativeWorkflowUsesTheCoverageSelector(t *testing.T) {
	t.Parallel()
	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "go-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateNativeWorkflowSelector(data); err != nil {
		t.Fatal(err)
	}
}
