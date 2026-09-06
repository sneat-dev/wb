package discover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanLocalIndexedReusesUnchangedObservation(t *testing.T) {
	projectsRoot := t.TempDir()
	mustIndexedRepository(t, projectsRoot, "acme", "widgets")
	cachePath := filepath.Join(t.TempDir(), "fleet-inventory.json")
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	options := LocalIndexOptions{CachePath: cachePath, MaxAge: time.Minute, Now: func() time.Time { return now }}

	first, err := ScanLocalIndexed(projectsRoot, options)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheHit || len(first.Repositories) != 1 || first.SourceFingerprint == "" {
		t.Fatalf("first index = %#v", first)
	}
	second, err := ScanLocalIndexed(projectsRoot, options)
	if err != nil {
		t.Fatal(err)
	}
	if !second.CacheHit || len(second.Repositories) != 1 || second.ObservedAt != now {
		t.Fatalf("second index = %#v", second)
	}
}

func TestScanLocalIndexedInvalidatesChangedOrganization(t *testing.T) {
	projectsRoot := t.TempDir()
	organization := filepath.Join(projectsRoot, "acme")
	mustIndexedRepository(t, projectsRoot, "acme", "one")
	cachePath := filepath.Join(t.TempDir(), "fleet-inventory.json")
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	options := LocalIndexOptions{CachePath: cachePath, MaxAge: time.Minute, Now: func() time.Time { return now }}
	if _, err := ScanLocalIndexed(projectsRoot, options); err != nil {
		t.Fatal(err)
	}
	mustIndexedRepository(t, projectsRoot, "acme", "two")
	changed := now.Add(time.Hour)
	if err := os.Chtimes(organization, changed, changed); err != nil {
		t.Fatal(err)
	}
	second, err := ScanLocalIndexed(projectsRoot, options)
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheHit || len(second.Repositories) != 2 {
		t.Fatalf("changed index = %#v", second)
	}
}

func TestFreshScanRemainsMutationAuthority(t *testing.T) {
	projectsRoot := t.TempDir()
	repository := mustIndexedRepository(t, projectsRoot, "acme", "widgets")
	cachePath := filepath.Join(t.TempDir(), "fleet-inventory.json")
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	options := LocalIndexOptions{CachePath: cachePath, MaxAge: time.Minute, Now: func() time.Time { return now }}
	if _, err := ScanLocalIndexed(projectsRoot, options); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repository, ".git")); err != nil {
		t.Fatal(err)
	}
	cached, err := ScanLocalIndexed(projectsRoot, options)
	if err != nil {
		t.Fatal(err)
	}
	if !cached.CacheHit || len(cached.Repositories) != 1 {
		t.Fatalf("read-only cache = %#v", cached)
	}
	fresh, err := ScanLocal(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 0 {
		t.Fatalf("fresh mutation discovery = %#v, want no repositories", fresh)
	}
}

func TestScanLocalIndexedRecoversCorruptCache(t *testing.T) {
	projectsRoot := t.TempDir()
	mustIndexedRepository(t, projectsRoot, "acme", "widgets")
	cachePath := filepath.Join(t.TempDir(), "fleet-inventory.json")
	if err := os.WriteFile(cachePath, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ScanLocalIndexed(projectsRoot, LocalIndexOptions{
		CachePath: cachePath, MaxAge: time.Minute,
		Now: func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheHit || len(result.Repositories) != 1 || len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0], "decode") {
		t.Fatalf("recovered index = %#v", result)
	}
}

func TestScanLocalIndexedExpiresObservation(t *testing.T) {
	projectsRoot := t.TempDir()
	mustIndexedRepository(t, projectsRoot, "acme", "widgets")
	cachePath := filepath.Join(t.TempDir(), "fleet-inventory.json")
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	options := LocalIndexOptions{CachePath: cachePath, MaxAge: time.Minute, Now: func() time.Time { return now }}
	if _, err := ScanLocalIndexed(projectsRoot, options); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute + time.Nanosecond)
	result, err := ScanLocalIndexed(projectsRoot, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheHit || !result.ObservedAt.Equal(now) {
		t.Fatalf("expired index = %#v", result)
	}
}

func mustIndexedRepository(t *testing.T, projectsRoot, organization, name string) string {
	t.Helper()
	repository := filepath.Join(projectsRoot, organization, name)
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repository
}
