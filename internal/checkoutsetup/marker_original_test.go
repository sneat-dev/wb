package checkoutsetup

import (
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"path/filepath"
	"testing"
)

func TestCwWtMarkerCheckoutsAndRegistration(t *testing.T) {
	t.Parallel()
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	testenv.CloneWithOrigin(t, seeds, "app", clone)
	linked := filepath.Join(projects, "linked")
	testenv.Git(t, clone, "worktree", "add", "-b", "b", linked)

	checkouts, err := NewMarkers(DefaultMarkerRunDependencies()).checkouts(t.Context(), MarkerRequest{Options: checkoutmarker.DescribeOptions{ProjectsRoot: projects}, Filter: "", Fleet: false, Paths: []string{"/one"}})
	if err != nil || len(checkouts) != 1 || checkouts[0] != "/one" {
		t.Fatalf("single checkout = (%v, %v)", checkouts, err)
	}
	checkouts, err = NewMarkers(DefaultMarkerRunDependencies()).checkouts(t.Context(), MarkerRequest{Options: checkoutmarker.DescribeOptions{ProjectsRoot: projects}, Filter: "", Fleet: false, Paths: nil})
	if err != nil || len(checkouts) != 1 || checkouts[0] != "." {
		t.Fatalf("default checkout = (%v, %v)", checkouts, err)
	}

	checkouts, err = NewMarkers(DefaultMarkerRunDependencies()).checkouts(t.Context(), MarkerRequest{Options: checkoutmarker.DescribeOptions{ProjectsRoot: projects}, Filter: "", Fleet: true, Paths: nil})
	if err != nil {
		t.Fatalf("fleet checkouts: %v", err)
	}
	if len(checkouts) != 2 {
		t.Fatalf("fleet checkouts = %v", checkouts)
	}
	checkouts, err = NewMarkers(DefaultMarkerRunDependencies()).checkouts(t.Context(), MarkerRequest{Options: checkoutmarker.DescribeOptions{ProjectsRoot: projects}, Filter: "nothing-matches", Fleet: true, Paths: nil})
	if err != nil || len(checkouts) != 0 {
		t.Fatalf("filtered fleet checkouts = (%v, %v)", checkouts, err)
	}
	checkouts, err = NewMarkers(DefaultMarkerRunDependencies()).checkouts(t.Context(), MarkerRequest{Options: checkoutmarker.DescribeOptions{ProjectsRoot: projects}, Filter: "acme/app", Fleet: true, Paths: nil})
	if err != nil || len(checkouts) != 2 {
		t.Fatalf("matching fleet checkouts = (%v, %v)", checkouts, err)
	}

	registered := registeredWorktrees(t.Context(), clone)
	if len(registered) != 1 {
		t.Fatalf("registered worktrees = %v", registered)
	}
	if got := registeredWorktrees(t.Context(), filepath.Join(projects, "not-a-clone")); got != nil {
		t.Fatalf("registered worktrees of a non-clone = %v", got)
	}

	// resolvedPath follows symlinks where it can and cleans where it cannot.
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolvedPath(link); got != resolvedTarget {
		t.Fatalf("resolvedPath(link) = %q, want %q", got, resolvedTarget)
	}
	if got := resolvedPath(filepath.Join(t.TempDir(), "missing")); !filepath.IsAbs(got) {
		t.Fatalf("resolvedPath(missing) = %q", got)
	}
}

func TestCwWtApplyCheckoutMarkerAndWouldChange(t *testing.T) {
	t.Parallel()
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	testenv.CloneWithOrigin(t, seeds, "app", clone)

	options := checkoutmarker.DescribeOptions{ProjectsRoot: projects, BaseBranch: "main", Version: "wb test"}
	outcome := applyMarker(clone, options, false, DefaultMarkerDependencies())
	if outcome.Error != "" {
		t.Fatalf("applyCheckoutMarker: %s", outcome.Error)
	}
	if !outcome.MarkerWritten || !outcome.ExcludeWritten {
		t.Fatalf("first apply outcome = %+v", outcome)
	}
	outcome = applyMarker(clone, options, false, DefaultMarkerDependencies())
	if outcome.MarkerWritten || outcome.ExcludeWritten {
		t.Fatalf("second apply outcome = %+v", outcome)
	}
	outcome = applyMarker(clone, options, true, DefaultMarkerDependencies())
	if outcome.MarkerWritten || outcome.ExcludeWritten {
		t.Fatalf("dry run after a write = %+v", outcome)
	}
	outcome = applyMarker(filepath.Join(t.TempDir(), "not-a-repo"), options, false, DefaultMarkerDependencies())
	if outcome.Error == "" {
		t.Fatal("applyCheckoutMarker on a non-checkout must record an error")
	}
	if outcome.Path == "" {
		t.Fatalf("a failed outcome must still name the path: %+v", outcome)
	}
}

func TestCwWtMarkerWouldChange(t *testing.T) {
	t.Parallel()
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	testenv.CloneWithOrigin(t, seeds, "app", clone)

	inspection, err := checkoutmarker.Describe(clone, checkoutmarker.DescribeOptions{ProjectsRoot: projects, BaseBranch: "main", Version: "wb test"})
	if err != nil {
		t.Fatal(err)
	}
	marker, exclude := markerWouldChange(inspection)
	if !marker || !exclude {
		t.Fatalf("fresh checkout would change = (%t, %t)", marker, exclude)
	}

	if outcome := applyMarker(clone, checkoutmarker.DescribeOptions{ProjectsRoot: projects, BaseBranch: "main", Version: "wb test"}, false, DefaultMarkerDependencies()); outcome.Error != "" {
		t.Fatal(outcome.Error)
	}
	inspection, err = checkoutmarker.Describe(clone, checkoutmarker.DescribeOptions{ProjectsRoot: projects, BaseBranch: "main", Version: "wb test"})
	if err != nil {
		t.Fatal(err)
	}
	marker, exclude = markerWouldChange(inspection)
	if marker || exclude {
		t.Fatalf("a current checkout would change = (%t, %t)", marker, exclude)
	}

	// A marker whose contents drifted is reported as changed.
	if err := os.WriteFile(filepath.Join(clone, checkoutmarker.FileName), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker, exclude = markerWouldChange(inspection)
	if !marker || exclude {
		t.Fatalf("drifted marker would change = (%t, %t)", marker, exclude)
	}

}
