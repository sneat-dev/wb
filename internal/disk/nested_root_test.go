package disk

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func categoriesByName(report Report) map[string]Category {
	byName := map[string]Category{}
	for _, category := range report.Categories {
		byName[category.Name] = category
	}
	return byName
}

// The retired $HOME/.wb keeps legacy checkouts in its worktrees/ directory, so
// a worktrees root sits inside the legacy home. A byte that lives in both
// trees must be attributed to one category, not reported by each: on
// 2026-11-27 that made the retired home look like 8.8 GB of WB state when 3.5
// GB of it was worktrees wb clean already owns.
func TestNestedWorktreeRootIsNotAlsoCountedAsLegacyHome(t *testing.T) {
	isolate(t)
	projects := t.TempDir()
	legacy := filepath.Join(os.Getenv("HOME"), ".wb")
	worktree := filepath.Join(legacy, "worktrees", "task")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 1<<20)
	if err := os.WriteFile(filepath.Join(worktree, "big.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "note"), []byte("hand-made"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Collect(context.Background(), Options{ProjectsRoot: projects})
	if err != nil {
		t.Fatal(err)
	}
	byName := categoriesByName(report)
	if byName["worktrees"].UnsharedBytes < int64(len(payload)) {
		t.Fatalf("worktrees reclaim = %d, want at least %d", byName["worktrees"].UnsharedBytes, len(payload))
	}
	legacyCategory, ok := byName["legacy-wb-home"]
	if !ok {
		t.Fatalf("no legacy-wb-home category: %#v", report.Categories)
	}
	if legacyCategory.UnsharedBytes >= int64(len(payload)) || legacyCategory.ApparentBytes >= int64(len(payload)) {
		t.Fatalf("legacy-wb-home also counts the worktree: reclaim %d, apparent %d", legacyCategory.UnsharedBytes, legacyCategory.ApparentBytes)
	}
	var sum int64
	for _, category := range report.Categories {
		sum += category.UnsharedBytes
	}
	if sum != report.AttributedBytes {
		t.Fatalf("category reclaim sums to %d, attributed %d", sum, report.AttributedBytes)
	}
}

// wb disk must measure the state directory and checkout store the projects
// root actually uses, and every canonical clone's own store at whatever depth
// its placement puts it, not only {org}/{repo}.
func TestDiskMeasuresTheCurrentLayout(t *testing.T) {
	isolate(t)
	projects := t.TempDir()
	clone := filepath.Join(projects, "github.com", "acme", "app")
	for _, dir := range []string{
		filepath.Join(projects, ".wb", "worklogs"),
		filepath.Join(projects, ".worktrees", "task", "github.com", "acme", "app"),
		filepath.Join(clone, ".worktrees", "local-task"),
		filepath.Join(os.Getenv("HOME"), ".wb", "reports", "x"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	restore := scanLocal
	scanLocal = func(root string) ([]discover.Repo, error) {
		return []discover.Repo{{Org: "acme", Name: "app", Host: "github.com", Path: clone, Local: true}}, nil
	}
	t.Cleanup(func() { scanLocal = restore })

	report, err := Collect(context.Background(), Options{ProjectsRoot: projects})
	if err != nil {
		t.Fatal(err)
	}
	byName := categoriesByName(report)
	if roots := byName["wb-state"].Roots; len(roots) != 1 || roots[0] != filepath.Join(projects, ".wb") {
		t.Fatalf("wb-state roots = %v, want the projects root's .wb", roots)
	}
	want := map[string]bool{
		filepath.Join(projects, ".worktrees"): false,
		filepath.Join(clone, ".worktrees"):    false,
	}
	for _, root := range byName["worktrees"].Roots {
		if _, ok := want[root]; ok {
			want[root] = true
		}
	}
	for root, found := range want {
		if !found {
			t.Fatalf("worktrees roots %v miss %s", byName["worktrees"].Roots, root)
		}
	}
	if roots := byName["legacy-wb-home"].Roots; len(roots) != 1 || roots[0] != filepath.Join(os.Getenv("HOME"), ".wb") {
		t.Fatalf("legacy-wb-home roots = %v", roots)
	}
}

// A retired home that is the state directory itself is not a second category.
func TestLegacyHomeIsOmittedWhenItIsTheStateDirectory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	legacy := filepath.Join(home, ".wb")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := legacyWBHome(home, legacy); got != "" {
		t.Fatalf("legacyWBHome = %q, want empty when it is the state directory", got)
	}
	if got := legacyWBHome("", legacy); got != "" {
		t.Fatalf("legacyWBHome without a user home = %q", got)
	}
	if got := legacyWBHome(t.TempDir(), legacy); got != "" {
		t.Fatalf("legacyWBHome for an absent directory = %q", got)
	}
	if got := legacyWBHome(home, t.TempDir()); got != legacy {
		t.Fatalf("legacyWBHome = %q, want %q", got, legacy)
	}
}

// A store found twice — once by clone discovery, once by the {org}/{repo}
// scan — is one root, and a discovered repository without a local path, or a
// discovery failure, adds nothing rather than a wrong root.
func TestCheckoutStoresAreDeduplicatedAndToleratesDiscoveryGaps(t *testing.T) {
	isolate(t)
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	if err := os.MkdirAll(filepath.Join(clone, ".worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	restore := scanLocal
	t.Cleanup(func() { scanLocal = restore })

	scanLocal = func(string) ([]discover.Repo, error) {
		return []discover.Repo{{Org: "acme", Name: "app", Path: clone}, {Org: "acme", Name: "remote-only"}}, nil
	}
	stores := checkoutStores(projects, "")
	count := 0
	for _, store := range stores {
		if store == filepath.Join(clone, ".worktrees") {
			count++
		}
	}
	if count != 1 || len(stores) != 2 {
		t.Fatalf("stores = %v, want the central store and the clone's store once", stores)
	}

	scanLocal = func(string) ([]discover.Repo, error) { return nil, os.ErrPermission }
	if stores := checkoutStores(projects, ""); len(stores) != 2 {
		t.Fatalf("stores after a discovery failure = %v, want the central store and the scanned layout", stores)
	}
}

// A state directory that cannot be resolved is an error, not a silent fallback
// to the retired home.
func TestCollectReportsAnUnresolvableStateDirectory(t *testing.T) {
	isolate(t)
	failure := os.ErrInvalid
	restore := resolveLayout
	resolveLayout = func(string) (wbhome.Resolution, error) { return wbhome.Resolution{}, failure }
	t.Cleanup(func() { resolveLayout = restore })
	if _, err := Collect(context.Background(), Options{ProjectsRoot: t.TempDir()}); err == nil {
		t.Fatal("unresolvable state directory was accepted")
	}
}
