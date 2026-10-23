package cmdworktree

import (
	"errors"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/checkoutsetup"
	"github.com/spf13/cobra"
	"strings"
	"testing"
)

type markerFailureWriter struct{ Allow, Writes int }

func (w *markerFailureWriter) Write(p []byte) (int, error) {
	if w.Writes >= w.Allow {
		return 0, errors.New("cwWt: injected write failure")
	}
	w.Writes++
	return len(p), nil
}
func TestCwWtMarkerSymbols(t *testing.T) {
	t.Parallel()

	if markerSymbol(checkoutsetup.MarkerOutcome{Kind: string(checkoutmarker.KindCanonical)}) != "🔒" {
		t.Fatal("a canonical checkout must use the lock symbol")
	}
	if markerSymbol(checkoutsetup.MarkerOutcome{Kind: "worktree"}) != "✎" {
		t.Fatal("a worktree must use the pencil symbol")
	}
}

func TestCwWtRenderMarkerOutcomes(t *testing.T) {
	t.Parallel()
	outcomes := []checkoutsetup.MarkerOutcome{
		{Path: "/a", Kind: "canonical", MarkerWritten: true, ExcludeWritten: true},
		{Path: "/b", Kind: "worktree", MarkerWritten: true},
		{Path: "/c", Kind: "worktree", ExcludeWritten: true},
		{Path: "/d", Kind: "worktree"},
		{Path: "/e", Kind: "worktree", Error: "boom"},
	}
	var out strings.Builder
	if err := renderMarkerOutcomes(&out, "text", false, outcomes); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"🔒 /a: wrote marker + ignore rule", "✎ /b: wrote marker",
		"✎ /c: wrote ignore rule", "✎ /d: current", "✗ /e: boom",
		"5 checkout(s), 3 changed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("marker text missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	if err := renderMarkerOutcomes(&out, "text", true, outcomes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would write marker + ignore rule") {
		t.Fatalf("dry-run marker text = %q", out.String())
	}

	out.Reset()
	if err := renderMarkerOutcomes(&out, "text", false, outcomes[:1]); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "checkout(s),") {
		t.Fatalf("a single outcome must not print a footer: %q", out.String())
	}

	out.Reset()
	if err := renderMarkerOutcomes(&out, "json", false, outcomes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\"marker_written\"") {
		t.Fatalf("marker json = %q", out.String())
	}

	for allow := 0; allow < 2; allow++ {
		command := &cobra.Command{}
		command.SetOut(&markerFailureWriter{Allow: allow})
		if err := renderMarkerOutcomes(command.OutOrStdout(), "text", false, outcomes); err == nil {
			t.Fatalf("renderMarkerOutcomes with %d writes allowed returned nil", allow)
		}
	}
}
