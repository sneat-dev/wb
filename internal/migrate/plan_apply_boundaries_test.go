package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanRetainsAbsoluteResolutionFailure(t *testing.T) {
	t.Parallel()
	cause := errors.New("current directory unavailable")
	plan, err := buildPlanWithAbsolute(migCovTextReplaceSpec("absolute"), func(string) (string, error) { return "", cause }, "relative-root")
	if !errors.Is(err, cause) || len(plan.Changes) != 0 {
		t.Fatalf("plan=%+v error=%v", plan, err)
	}
}

func TestApplyReportsNativeStatFailureAfterReadingPlannedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "source.py")
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(migCovTextReplaceSpec("read-race"), root)
	if err != nil {
		t.Fatal(err)
	}
	retained := path + "-retained"
	err = applyWithRead(plan, nil, func(path string) ([]byte, error) {
		raw, err := os.ReadFile(path)
		if err == nil {
			if err := os.Rename(path, retained); err != nil {
				t.Fatal(err)
			}
		}
		return raw, err
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error=%v", err)
	}
	raw, err := os.ReadFile(retained)
	if err != nil || string(raw) != "old\n" {
		t.Fatalf("original=%q error=%v", raw, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary outputs=%v error=%v", entries, err)
	}
}

func TestGoCompositeRenamePreservesUnkeyedElements(t *testing.T) {
	t.Parallel()
	source := []byte("package p\ntype Pair struct { Old int; Other int }\nvar _ = Pair{1, 2}\nvar _ = Pair{Old: 3}\n")
	out, changed, err := transformGo(source, "pair.go", Step{Kind: "composite_field.rename", From: "Old", To: "New"})
	if err != nil || !changed || !strings.Contains(string(out), "Pair{1, 2}") || !strings.Contains(string(out), "New: 3") {
		t.Fatalf("changed=%v error=%v output=%s", changed, err, out)
	}
}

func TestGoRenameReportsNativeFormatterFailure(t *testing.T) {
	t.Parallel()
	// Unsorted imports require go/format's native reparsing pass. An invalid
	// requested field name must report that failure instead of returning bytes.
	source := []byte("package p\nimport (\n\"z\"\n\"a\"\n)\nvar _ = z.Pair{Old: 1}\nvar _ = a.Value\n")
	_, changed, err := transformGo(source, "pair.go", Step{Kind: "composite_field.rename", From: "Old", To: "bad name"})
	if err == nil || changed || !strings.Contains(err.Error(), "format Go source") {
		t.Fatalf("changed=%v error=%v", changed, err)
	}
}
