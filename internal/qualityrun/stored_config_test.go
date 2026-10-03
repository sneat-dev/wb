package qualityrun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/quality"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRepositorySlug(t *testing.T) {
	// Inside the current repo, empty or dot resolves to the repo's origin slug (e.g. sneat-dev/wb)
	gotDot := resolveRepositorySlug(".", func(string) (string, error) { return "https://github.com/sneat-dev/wb.git", nil })
	if gotDot != "sneat-dev/wb" {
		t.Errorf("resolveRepositorySlug('.') = %q, want fixture folder identity", gotDot)
	}
	gotEmpty := resolveRepositorySlug("", func(string) (string, error) { return "https://github.com/sneat-dev/wb.git", nil })
	if gotEmpty != gotDot {
		t.Errorf("resolveRepositorySlug('') = %q, want %q", gotEmpty, gotDot)
	}

	// Plain dir without git repo
	plainDir := t.TempDir()
	if got := resolveRepositorySlug(plainDir, nil); got != filepath.Base(plainDir) {
		t.Errorf("resolveRepositorySlug(%q) = %q, want %q", plainDir, got, filepath.Base(plainDir))
	}

	// Arbitrary non-existent slug passed directly
	if got := resolveRepositorySlug("org/custom-repo", nil); got != "org/custom-repo" {
		t.Errorf("resolveRepositorySlug('org/custom-repo') = %q, want 'org/custom-repo'", got)
	}

	// Directory with origin remote URL
	gitDir := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var originFn func(string) (string, error)

	originFn = func(path string) (string, error) {
		return "https://github.com/sneat-co/myrepo.git", nil
	}
	if got := resolveRepositorySlug(gitDir, originFn); got != "sneat-co/myrepo" {
		t.Errorf("resolveRepositorySlug(gitDir,originFn) = %q, want 'sneat-co/myrepo'", got)
	}

	// Origin URL returns invalid remote
	originFn = func(path string) (string, error) {
		return "invalid-url", nil
	}
	if got := resolveRepositorySlug(gitDir, originFn); got != filepath.Base(gitDir) {
		t.Errorf("resolveRepositorySlug(gitDir,originFn) with invalid url = %q, want %q", got, filepath.Base(gitDir))
	}

	// Origin URL returns error
	originFn = func(path string) (string, error) {
		return "", errors.New("no origin")
	}
	if got := resolveRepositorySlug(gitDir, originFn); got != filepath.Base(gitDir) {
		t.Errorf("resolveRepositorySlug(gitDir,originFn) with origin error = %q, want %q", got, filepath.Base(gitDir))
	}

	// Fallback to gitops.OriginURL when originURL func is nil
	originFn = nil
	if got := resolveRepositorySlug(gitDir, originFn); got != filepath.Base(gitDir) {
		t.Errorf("resolveRepositorySlug(gitDir,originFn) with nil originURL = %q, want %q", got, filepath.Base(gitDir))
	}
}
func TestCoverageReportFromStored_DefaultStatus(t *testing.T) {
	stored := []hub.StoredRepositoryCoverage{
		{
			Repository: "sneat-dev/wb",
			Status:     "",
			Statements: 10,
			Covered:    10,
			Percentage: 100.0,
		},
	}
	report := coverageReportFromStored(stored, "")
	if len(report.Repositories) != 1 || report.Repositories[0].Status != quality.StatusPassed {
		t.Errorf("report status = %v, want StatusPassed", report.Repositories[0].Status)
	}

	// Exercise filter != "" continue branch
	filtered := coverageReportFromStored(stored, "non-matching-filter")
	if len(filtered.Repositories) != 0 {
		t.Errorf("expected 0 repositories, got %d", len(filtered.Repositories))
	}
}
func TestOpenDefaultCoverageStore(t *testing.T) {
	ctx := context.Background()

	// 1. With config file specifying memory engine
	configDir := t.TempDir()
	wbDir := filepath.Join(configDir, "wb")
	if err := os.MkdirAll(wbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgContent := "hub:\n  store:\n    engine: memory\n"
	if err := os.WriteFile(filepath.Join(wbDir, "wb.yaml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configDir)
	store, closer, err := openDefaultCoverageStore(ctx)
	if err != nil || store == nil {
		t.Fatalf("openDefaultCoverageStore with memory config failed: %v", err)
	}
	if closer != nil {
		_ = closer.Close()
	}

	// 2. With no config and no ~/.wb/hub directory
	emptyHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(emptyHome, "nonexistent"))
	t.Setenv("HOME", emptyHome)
	_, _, err = openDefaultCoverageStore(ctx)
	if err == nil {
		t.Error("expected error when no store is configured, got nil")
	}

	// 3. With ~/.wb/hub directory existing
	inGitHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inGitHome, ".wb", "hub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(inGitHome, "nonexistent"))
	t.Setenv("HOME", inGitHome)
	store, closer, err = openDefaultCoverageStore(ctx)
	if err != nil || store == nil {
		t.Fatalf("openDefaultCoverageStore with inGitDB dir failed: %v", err)
	}
	if closer != nil {
		_ = closer.Close()
	}
}
func TestPublicStoredCoverageUnavailable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if _, err := StoredCoverage(t.Context(), StoredRequest{Fleet: true}); err == nil {
		t.Fatal("missing store accepted")
	}
	if err := (nopCloser{}).Close(); err != nil {
		t.Fatal(err)
	}
}
