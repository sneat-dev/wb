package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
	"github.com/sneat-dev/wb/internal/quality"
)

func setupTestCoverageStore(t *testing.T) hub.RepositoryCoverageStore {
	ctx := context.Background()
	db, closer, err := hubstore.Open(ctx, hubconfig.Store{Engine: hubconfig.EngineMemory})
	if err != nil {
		t.Fatalf("hubstore.Open memory: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	return hub.NewRepositoryCoverageStore(db)
}

func mockCoverageStore(t *testing.T, store hub.RepositoryCoverageStore) {
	orig := defaultCoverageDeps
	t.Cleanup(func() { defaultCoverageDeps = orig })
	defaultCoverageDeps = coverageDeps{
		openStore: func(ctx context.Context) (hub.RepositoryCoverageStore, io.Closer, error) {
			return store, nopCloser{}, nil
		},
	}
}

func TestFleetCoverageCmd_Empty(t *testing.T) {
	store := setupTestCoverageStore(t)
	mockCoverageStore(t, store)

	var stdout, stderr bytes.Buffer
	code := run([]string{"fleet", "coverage"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("run(fleet coverage) = %d, want %d; stderr: %s", code, exitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no CI coverage reports collected yet") {
		t.Errorf("stdout = %q, want 'no CI coverage reports collected yet'", stdout.String())
	}
}

func TestFleetCoverageCmd_Populated(t *testing.T) {
	store := setupTestCoverageStore(t)
	mockCoverageStore(t, store)
	ctx := context.Background()

	now := time.Now().UTC()
	records := []hub.StoredRepositoryCoverage{
		{
			Repository: "sneat-dev/wb",
			SHA:        "1111111111111111111111111111111111111111",
			Ref:        "refs/heads/main",
			ReportedAt: now,
			Status:     quality.StatusPassed,
			Statements: 1000,
			Covered:    850,
			Percentage: 85.0,
			Modules: []quality.ModuleCoverageSummary{
				{Path: ".", Statements: 1000, Covered: 850, Percentage: 85.0},
			},
		},
		{
			Repository: "sneat-co/app",
			SHA:        "2222222222222222222222222222222222222222",
			Ref:        "refs/heads/main",
			ReportedAt: now,
			Status:     quality.StatusPassed,
			Statements: 500,
			Covered:    450,
			Percentage: 90.0,
			Modules: []quality.ModuleCoverageSummary{
				{Path: ".", Statements: 500, Covered: 450, Percentage: 90.0},
			},
		},
	}
	for _, r := range records {
		if err := store.SaveCoverage(ctx, r); err != nil {
			t.Fatalf("SaveCoverage: %v", err)
		}
	}

	// 1. Default markdown format
	var stdout, stderr bytes.Buffer
	code := run([]string{"fleet", "coverage"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("run(fleet coverage) = %d, want %d; stderr: %s", code, exitOK, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "`sneat-dev/wb`") || !strings.Contains(out, "`sneat-co/app`") {
		t.Errorf("expected both repositories in output, got:\n%s", out)
	}
	if !strings.Contains(out, "**Fleet total:**") {
		t.Errorf("expected fleet total in markdown output, got:\n%s", out)
	}

	// 2. JSON format
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"fleet", "coverage", "--format", "json"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("run(fleet coverage --format json) = %d; stderr: %s", code, stderr.String())
	}
	var jsonReport quality.CoverageReport
	if err := json.Unmarshal(stdout.Bytes(), &jsonReport); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(jsonReport.Repositories) != 2 {
		t.Errorf("len(Repositories) = %d, want 2", len(jsonReport.Repositories))
	}
	if jsonReport.Statements != 1500 || jsonReport.Covered != 1300 {
		t.Errorf("Statements/Covered = %d/%d, want 1500/1300", jsonReport.Statements, jsonReport.Covered)
	}

	// 3. YAML format
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"fleet", "coverage", "--format", "yaml"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("run(fleet coverage --format yaml) = %d; stderr: %s", code, stderr.String())
	}
	var yamlReport quality.CoverageReport
	if err := yaml.Unmarshal(stdout.Bytes(), &yamlReport); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if len(yamlReport.Repositories) != 2 {
		t.Errorf("yaml len(Repositories) = %d, want 2", len(yamlReport.Repositories))
	}

	// 4. Report dir
	reportDir := t.TempDir()
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"fleet", "coverage", "--report-dir", reportDir}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("run(fleet coverage --report-dir) = %d; stderr: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(reportDir, "coverage.md")); err != nil {
		t.Errorf("coverage.md not created in %s", reportDir)
	}
	if _, err := os.Stat(filepath.Join(reportDir, "coverage.yaml")); err != nil {
		t.Errorf("coverage.yaml not created in %s", reportDir)
	}
}

func TestFleetCoverageCmd_Filtering(t *testing.T) {
	store := setupTestCoverageStore(t)
	mockCoverageStore(t, store)
	ctx := context.Background()

	_ = store.SaveCoverage(ctx, hub.StoredRepositoryCoverage{
		Repository: "sneat-dev/wb",
		Statements: 100, Covered: 80, Percentage: 80.0,
	})
	_ = store.SaveCoverage(ctx, hub.StoredRepositoryCoverage{
		Repository: "sneat-co/app",
		Statements: 100, Covered: 90, Percentage: 90.0,
	})

	// Match sneat-co/*
	var stdout, stderr bytes.Buffer
	code := run([]string{"fleet", "coverage", "--match", "sneat-co/*"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("run --match sneat-co/* = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "sneat-co/app") || strings.Contains(stdout.String(), "sneat-dev/wb") {
		t.Errorf("unexpected filtered output:\n%s", stdout.String())
	}

	// Regex .*wb$
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"fleet", "coverage", "--regex", ".*wb$"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("run --regex = %d; stderr: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "sneat-co/app") || !strings.Contains(stdout.String(), "sneat-dev/wb") {
		t.Errorf("unexpected regex output:\n%s", stdout.String())
	}

	// No match
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"fleet", "coverage", "--match", "other/*"}, &stdout, &stderr)
	if code == exitOK {
		t.Errorf("expected error for non-matching filter, got exitOK")
	}

	// Invalid regex
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"fleet", "coverage", "--regex", "["}, &stdout, &stderr)
	if code == exitOK {
		t.Errorf("expected error for invalid regex, got exitOK")
	}
}

func TestCoverageCmd_CI_SingleRepo(t *testing.T) {
	store := setupTestCoverageStore(t)
	mockCoverageStore(t, store)
	ctx := context.Background()

	_ = store.SaveCoverage(ctx, hub.StoredRepositoryCoverage{
		Repository: "sneat-dev/wb",
		Statements: 200, Covered: 180, Percentage: 90.0,
	})

	// Full slug
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "sneat-dev/wb", "--ci"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("run(coverage sneat-dev/wb --ci) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "sneat-dev/wb") || !strings.Contains(stdout.String(), "90.00%") {
		t.Errorf("unexpected output:\n%s", stdout.String())
	}

	// Suffix match
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"coverage", "wb", "--ci"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("run(coverage wb --ci) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "sneat-dev/wb") {
		t.Errorf("unexpected output for suffix match:\n%s", stdout.String())
	}

	// Non-existent repo
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"coverage", "nonexistent-repo", "--ci"}, &stdout, &stderr)
	if code == exitOK {
		t.Errorf("expected failure for non-existent repo, got exitOK")
	}
	if !strings.Contains(stderr.String(), "no CI coverage found for repository nonexistent-repo") {
		t.Errorf("stderr = %q, want 'no CI coverage found...'", stderr.String())
	}
}

func TestCoverageCmd_CI_Fleet(t *testing.T) {
	store := setupTestCoverageStore(t)
	mockCoverageStore(t, store)
	ctx := context.Background()

	_ = store.SaveCoverage(ctx, hub.StoredRepositoryCoverage{
		Repository: "sneat-dev/wb",
		Statements: 200, Covered: 180, Percentage: 90.0,
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "--ci", "--fleet"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("run(coverage --ci --fleet) = %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "sneat-dev/wb") {
		t.Errorf("unexpected output:\n%s", stdout.String())
	}
}

func TestCoverageCmd_CI_MinimumGate(t *testing.T) {
	store := setupTestCoverageStore(t)
	mockCoverageStore(t, store)
	ctx := context.Background()

	_ = store.SaveCoverage(ctx, hub.StoredRepositoryCoverage{
		Repository: "sneat-dev/wb",
		Statements: 100, Covered: 85, Percentage: 85.0,
	})

	// Minimum met (80 <= 85)
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "sneat-dev/wb", "--ci", "--minimum", "80"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("run(coverage --ci --minimum 80) = %d; stderr: %s", code, stderr.String())
	}

	// Minimum failed (90 > 85)
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"coverage", "sneat-dev/wb", "--ci", "--minimum", "90"}, &stdout, &stderr)
	if code != exitFindings {
		t.Fatalf("run(coverage --ci --minimum 90) = %d, want exitFindings (%d)", code, exitFindings)
	}
	if !strings.Contains(stderr.String(), "is below required 90.00%") {
		t.Errorf("stderr = %q, want 'is below required 90.00%%'", stderr.String())
	}
}

func TestCoverageCmd_CI_FlagValidation(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// --ci with --changed
	code := run([]string{"coverage", "--ci", "--changed", "--target", "main"}, &stdout, &stderr)
	if code != exitUsage {
		t.Errorf("run(--ci --changed) = %d, want exitUsage (%d); stderr: %s", code, exitUsage, stderr.String())
	}

	// --ci with --resume
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"coverage", "--ci", "--resume"}, &stdout, &stderr)
	if code == exitOK {
		t.Error("expected error for --ci with --resume")
	}

	// --ci with --coverage-profile
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"coverage", "--ci", "--coverage-profile", "out.cov"}, &stdout, &stderr)
	if code == exitOK {
		t.Error("expected error for --ci with --coverage-profile")
	}

	// --ci with --test-shards
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"coverage", "--ci", "--test-shards", "2", "--shard-package", "pkg"}, &stdout, &stderr)
	if code == exitOK {
		t.Error("expected error for --ci with --test-shards")
	}

	// --ci with --shard-package
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"coverage", "--ci", "--shard-package", "pkg"}, &stdout, &stderr)
	if code == exitOK {
		t.Error("expected error for --ci with --shard-package")
	}
}

func TestCoverageCmd_CI_StoreUnavailable(t *testing.T) {
	orig := defaultCoverageDeps
	t.Cleanup(func() { defaultCoverageDeps = orig })
	defaultCoverageDeps = coverageDeps{
		openStore: func(ctx context.Context) (hub.RepositoryCoverageStore, io.Closer, error) {
			return nil, nil, errors.New("simulated store failure")
		},
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"fleet", "coverage"}, &stdout, &stderr)
	if code == exitOK {
		t.Error("expected failure when store unavailable, got exitOK")
	}
	if !strings.Contains(stderr.String(), "simulated store failure") {
		t.Errorf("stderr = %q, want 'simulated store failure'", stderr.String())
	}
}

type errCoverageStore struct {
	listErr error
	getErr  error
}

func (e errCoverageStore) SaveCoverage(ctx context.Context, record hub.StoredRepositoryCoverage) error {
	return nil
}
func (e errCoverageStore) GetCoverage(ctx context.Context, repository string) (hub.StoredRepositoryCoverage, bool, error) {
	return hub.StoredRepositoryCoverage{}, false, e.getErr
}
func (e errCoverageStore) ListCoverage(ctx context.Context) ([]hub.StoredRepositoryCoverage, error) {
	return nil, e.listErr
}

func TestCoverageCmd_StoreErrors(t *testing.T) {
	orig := defaultCoverageDeps
	t.Cleanup(func() { defaultCoverageDeps = orig })

	// ListCoverage error
	defaultCoverageDeps = coverageDeps{
		openStore: func(ctx context.Context) (hub.RepositoryCoverageStore, io.Closer, error) {
			return errCoverageStore{listErr: errors.New("list coverage boom")}, nopCloser{}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"fleet", "coverage"}, &stdout, &stderr)
	if code == exitOK {
		t.Error("expected error for ListCoverage failure, got exitOK")
	}

	// GetCoverage error
	defaultCoverageDeps = coverageDeps{
		openStore: func(ctx context.Context) (hub.RepositoryCoverageStore, io.Closer, error) {
			return errCoverageStore{getErr: errors.New("get coverage boom")}, nopCloser{}, nil
		},
	}
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"coverage", "wb", "--ci"}, &stdout, &stderr)
	if code == exitOK {
		t.Error("expected error for GetCoverage failure, got exitOK")
	}
}

func TestCoverageCmd_InvalidFormat(t *testing.T) {
	store := setupTestCoverageStore(t)
	mockCoverageStore(t, store)
	ctx := context.Background()

	_ = store.SaveCoverage(ctx, hub.StoredRepositoryCoverage{
		Repository: "sneat-dev/wb",
		Statements: 100, Covered: 80, Percentage: 80.0,
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"fleet", "coverage", "--format", "unsupported-format"}, &stdout, &stderr)
	if code == exitOK {
		t.Error("expected error for invalid format in fleet mode")
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"coverage", "wb", "--ci", "--format", "unsupported-format"}, &stdout, &stderr)
	if code == exitOK {
		t.Error("expected error for invalid format in single-repo mode")
	}
}

func TestResolveRepositorySlug(t *testing.T) {
	// Inside the current repo, empty or dot resolves to the repo's origin slug (e.g. sneat-dev/wb)
	gotDot := resolveRepositorySlug(".")
	if !strings.Contains(gotDot, "wb") {
		t.Errorf("resolveRepositorySlug('.') = %q, want repo slug containing 'wb'", gotDot)
	}
	gotEmpty := resolveRepositorySlug("")
	if gotEmpty != gotDot {
		t.Errorf("resolveRepositorySlug('') = %q, want %q", gotEmpty, gotDot)
	}

	// Plain dir without git repo
	plainDir := t.TempDir()
	if got := resolveRepositorySlug(plainDir); got != filepath.Base(plainDir) {
		t.Errorf("resolveRepositorySlug(%q) = %q, want %q", plainDir, got, filepath.Base(plainDir))
	}

	// Arbitrary non-existent slug passed directly
	if got := resolveRepositorySlug("org/custom-repo"); got != "org/custom-repo" {
		t.Errorf("resolveRepositorySlug('org/custom-repo') = %q, want 'org/custom-repo'", got)
	}

	// Directory with origin remote URL
	gitDir := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := defaultCoverageDeps
	t.Cleanup(func() { defaultCoverageDeps = orig })

	defaultCoverageDeps.originURL = func(path string) (string, error) {
		return "https://github.com/sneat-co/myrepo.git", nil
	}
	if got := resolveRepositorySlug(gitDir); got != "sneat-co/myrepo" {
		t.Errorf("resolveRepositorySlug(gitDir) = %q, want 'sneat-co/myrepo'", got)
	}

	// Origin URL returns invalid remote
	defaultCoverageDeps.originURL = func(path string) (string, error) {
		return "invalid-url", nil
	}
	if got := resolveRepositorySlug(gitDir); got != filepath.Base(gitDir) {
		t.Errorf("resolveRepositorySlug(gitDir) with invalid url = %q, want %q", got, filepath.Base(gitDir))
	}

	// Origin URL returns error
	defaultCoverageDeps.originURL = func(path string) (string, error) {
		return "", errors.New("no origin")
	}
	if got := resolveRepositorySlug(gitDir); got != filepath.Base(gitDir) {
		t.Errorf("resolveRepositorySlug(gitDir) with origin error = %q, want %q", got, filepath.Base(gitDir))
	}

	// Fallback to gitops.OriginURL when originURL func is nil
	defaultCoverageDeps.originURL = nil
	if got := resolveRepositorySlug(gitDir); got != filepath.Base(gitDir) {
		t.Errorf("resolveRepositorySlug(gitDir) with nil originURL = %q, want %q", got, filepath.Base(gitDir))
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
