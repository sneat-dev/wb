package disk

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectReportsMissingHomeAndFilesystemFailure(t *testing.T) {
	t.Parallel()
	missingHome := errors.New("home unavailable")
	if _, err := collectWithDiscovery(context.Background(), Options{ProjectsRoot: t.TempDir()}, func() (string, error) { return "", missingHome }, candidateGroups); !errors.Is(err, missingHome) || !strings.Contains(err.Error(), "resolve home directory") {
		t.Fatalf("missing home: %v", err)
	}
	failure := errors.New("volume unavailable")
	_, err := Collect(context.Background(), Options{ProjectsRoot: t.TempDir(), WBHome: t.TempDir(), FilesystemProbe: func(string) (Filesystem, error) { return Filesystem{}, failure }})
	if !errors.Is(err, failure) {
		t.Fatalf("filesystem probe: %v", err)
	}
}

func TestCollectReportsCancelledMeasurementsAndOmitsUnknownCaches(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := collectWithDiscovery(ctx, Options{ProjectsRoot: root, WBHome: home, FilesystemProbe: func(path string) (Filesystem, error) {
		return Filesystem{Path: path, TotalBytes: 100, AvailableBytes: 50}, nil
	}}, os.UserHomeDir, func(string, string) []group {
		return []group{{name: "go-build-cache", roots: []string{""}}, {name: "go-module-cache", roots: []string{""}}, {name: "wb-state", roots: []string{home}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 1 || !strings.Contains(report.Skipped[0], "context canceled") {
		t.Fatalf("skipped=%v", report.Skipped)
	}
	if report.AttributedBytes != 0 {
		t.Fatalf("attributed=%d", report.AttributedBytes)
	}
	for _, category := range report.Categories {
		if category.Name == "go-build-cache" || category.Name == "go-module-cache" {
			t.Fatalf("unknown cache reported: %+v", category)
		}
	}
}

func TestWorktreeRootsHandlesReadFailures(t *testing.T) {
	t.Parallel()
	if roots := worktreeRoots(filepath.Join(t.TempDir(), "missing")); len(roots) != 0 {
		t.Fatalf("roots=%v", roots)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "blocked"), 0700); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("directory unreadable")
	roots := worktreeRootsWithReadDir(root, func(path string) ([]os.DirEntry, error) {
		if path != root {
			return nil, failure
		}
		return os.ReadDir(path)
	})
	if len(roots) != 0 {
		t.Fatalf("roots=%v", roots)
	}
}

func TestFilesystemForRejectsMissingPath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing")
	got, err := filesystemFor(path)
	if err == nil || !strings.Contains(err.Error(), "stat filesystem at "+path) || got.Path != "" {
		t.Fatalf("filesystem=%+v,%v", got, err)
	}
}

func TestCollectSortsMeasuredCategoriesAndSkipsSizesForPresentRoots(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	worktree := filepath.Join(root, "acme", "app", ".worktrees")
	if err := os.MkdirAll(worktree, 0700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{filepath.Join(worktree, "small"): "a", filepath.Join(home, "large"): strings.Repeat("b", 16<<10)} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	options := Options{ProjectsRoot: root, WBHome: home, FilesystemProbe: func(path string) (Filesystem, error) {
		return Filesystem{Path: path, TotalBytes: 1 << 20, AvailableBytes: 1 << 19}, nil
	}}
	groups := func(string, string) []group {
		return []group{{name: "worktrees", roots: []string{worktree}}, {name: "wb-state", roots: []string{home}}}
	}
	measured, err := collectWithDiscovery(context.Background(), options, os.UserHomeDir, groups)
	if err != nil {
		t.Fatal(err)
	}
	if len(measured.Categories) != 2 || measured.Categories[0].Name != "wb-state" || measured.Categories[0].UnsharedBytes <= measured.Categories[1].UnsharedBytes {
		t.Fatalf("measured categories=%+v", measured.Categories)
	}
	options.SkipSizes = true
	skipped, err := collectWithDiscovery(context.Background(), options, os.UserHomeDir, groups)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped.Categories) != 2 || skipped.AttributedBytes != 0 {
		t.Fatalf("skip sizes=%+v", skipped)
	}
	for _, category := range skipped.Categories {
		if len(category.Roots) != 1 || category.ApparentBytes != 0 || category.UnsharedBytes != 0 {
			t.Fatalf("skipped category=%+v", category)
		}
	}
}

func TestGoEnvOmitsCacheRootWhenToolchainCannotRun(t *testing.T) {
	t.Parallel()
	calls := 0
	got := goEnvWithOutput(func() ([]byte, error) { calls++; return nil, errors.New("toolchain unavailable") })
	if got != "" || calls != 1 {
		t.Fatalf("unknown cache root=%q,command calls=%d", got, calls)
	}
}
