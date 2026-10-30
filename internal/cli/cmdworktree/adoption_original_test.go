package cmdworktree

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestCwWtRenderAdopt(t *testing.T) {
	t.Parallel()
	results := []worktrees.AdoptResult{
		{Path: "/tmp/a", Task: "t", Action: worktrees.AdoptAdopted},
		{Path: "/tmp/b", Task: "t", Action: worktrees.AdoptWouldAdopt},
		{Path: "/tmp/c", Action: worktrees.AdoptSkipped, Reason: "already managed"},
	}
	var out bytes.Buffer
	if err := writeAdoption(&out, results, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"skipped /tmp/c: already managed",
		"adopted", "/tmp/a", "would_adopt", "/tmp/b",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("adopt output missing %q:\n%s", want, text)
		}
	}
	out.Reset()
	if err := writeAdoption(&out, results, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "dry-run only") {
		t.Fatalf("adopt apply output = %q", out.String())
	}
	out.Reset()
	if err := writeAdoption(&out, nil, true); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("empty adopt output = %q", out.String())
	}

	for allow := 0; allow < 3; allow++ {
		if err := writeAdoption(&activeLimitedWriter{Allow: allow}, results, false); err == nil {
			t.Fatalf("writeAdoption with %d writes allowed returned nil", allow)
		}
	}
}

func TestAdoptionOriginalWriterSweep(t *testing.T) {
	t.Parallel()
	adopt := []worktrees.AdoptResult{
		{Path: "/tmp/a", Task: "t", Action: worktrees.AdoptAdopted},
		{Path: "/tmp/b", Task: "t", Action: worktrees.AdoptWouldAdopt},
		{Path: "/tmp/c", Action: worktrees.AdoptSkipped, Reason: "already managed"},
	}
	activeSweepWrites(t, 10, func(writer *activeLimitedWriter) error {
		return writeAdoption(writer, adopt, false)
	})
}
