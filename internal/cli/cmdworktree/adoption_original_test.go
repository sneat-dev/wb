package cmdworktree

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/worktrees"
	"strings"
	"testing"
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

func TestCwWtRenderOrphans(t *testing.T) {
	t.Parallel()
	report := worktrees.OrphanReport{
		Families: []worktrees.OrphanFamily{
			{
				RootEffort: "effort-1", Disposition: worktrees.DispositionRemove, Reason: "every worktree landed",
				Worktrees: []worktrees.OrphanWorktree{
					{Disposition: "remove", Repository: "acme/a", Branch: "b", Layout: worktrees.LayoutCurrent, HasManifest: false, Dirty: true, Missing: true, OwnerState: worktrees.OwnerLive, Evidence: []string{"landed"}},
					{Disposition: "remove", Repository: "acme/b", Branch: "b", Layout: worktrees.LayoutCurrent, HasManifest: true, Provenance: "reconstructed", OwnerState: worktrees.OwnerGone},
					{Disposition: "remove", Repository: "acme/c", Branch: "b", Layout: worktrees.LayoutLegacy, HasManifest: true, OwnerState: ""},
				},
			},
			{RootEffort: "effort-2", Disposition: worktrees.DispositionReview, Reason: "needs a look"},
		},
		Residue: []worktrees.OrphanResidue{
			{Task: "task", Repository: "acme/a", Layout: worktrees.LayoutLocal, Evidence: []string{"unregistered"}, Remedy: "wb worktree gc"},
		},
		Totals: worktrees.OrphanTotals{
			Worktrees: 4, Families: 2,
			ByLayout:    map[string]int{worktrees.LayoutCurrent: 2, worktrees.LayoutLegacy: 1, worktrees.LayoutExternal: 1},
			ByDispositn: map[string]int{worktrees.DispositionRemove: 1, worktrees.DispositionReview: 1},
			NoManifest:  1, Dirty: 1, Residue: 1,
		},
		Unscanned: []string{"/tmp/unreadable"},
	}
	var out bytes.Buffer
	if err := writeOrphans(&out, report, ""); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"effort-1 [remove] every worktree landed",
		"no-manifest", "reconstructed", "dirty", "missing", "owner live", "owner gone", "owner unstated",
		"- landed", "unregistered checkouts (1)", "wb worktree gc",
		"4 worktrees in 2 efforts (2 shown): 2 current, 1 legacy, 1 external; 1 without a manifest, 1 dirty",
		"unscanned: /tmp/unreadable",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("orphans output missing %q:\n%s", want, text)
		}
	}

	// --only filters both families and the residue section.
	out.Reset()
	if err := writeOrphans(&out, report, worktrees.DispositionRemove); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	if strings.Contains(text, "effort-2") || strings.Contains(text, "unregistered checkouts") {
		t.Fatalf("--only=remove output = %q", text)
	}
	out.Reset()
	if err := writeOrphans(&out, report, worktrees.DispositionReview); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	if strings.Contains(text, "effort-1") || !strings.Contains(text, "unregistered checkouts") {
		t.Fatalf("--only=review output = %q", text)
	}

	// A report with no families and no residue still prints the footer.
	out.Reset()
	if err := writeOrphans(&out, worktrees.OrphanReport{}, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(0 shown)") {
		t.Fatalf("empty orphans output = %q", out.String())
	}

	for allow := 0; allow < 3; allow++ {
		if err := writeOrphans(&activeLimitedWriter{Allow: allow}, report, ""); err == nil {
			t.Fatalf("writeOrphans with %d writes allowed returned nil", allow)
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

func TestCwWtWriterSweepOrphansAndActive(t *testing.T) {
	t.Parallel()
	report := worktrees.OrphanReport{
		Families: []worktrees.OrphanFamily{
			{
				RootEffort: "e1", Disposition: worktrees.DispositionRemove, Reason: "landed",
				Worktrees: []worktrees.OrphanWorktree{
					{Disposition: "remove", Repository: "acme/a", Branch: "b", Layout: worktrees.LayoutCurrent, HasManifest: false, Dirty: true, Missing: true, OwnerState: worktrees.OwnerLive, Evidence: []string{"one"}},
					{Disposition: "remove", Repository: "acme/b", Branch: "b", Layout: worktrees.LayoutLegacy, HasManifest: true, Provenance: "reconstructed", OwnerState: worktrees.OwnerGone},
					{Disposition: "remove", Repository: "acme/c", Branch: "b", Layout: worktrees.LayoutExternal, HasManifest: true},
				},
			},
		},
		Residue: []worktrees.OrphanResidue{{Task: "t", Repository: "acme/a", Layout: worktrees.LayoutLocal, Evidence: []string{"unregistered"}, Remedy: "wb worktree gc"}},
		Totals: worktrees.OrphanTotals{
			Worktrees: 3, Families: 1,
			ByLayout:    map[string]int{worktrees.LayoutCurrent: 1, worktrees.LayoutLegacy: 1, worktrees.LayoutExternal: 1},
			ByDispositn: map[string]int{worktrees.DispositionRemove: 1},
			NoManifest:  1, Dirty: 1, Residue: 1,
		},
		Unscanned: []string{"/tmp/unreadable"},
	}
	activeSweepWrites(t, 40, func(writer *activeLimitedWriter) error {
		return writeOrphans(writer, report, "")
	})

}
