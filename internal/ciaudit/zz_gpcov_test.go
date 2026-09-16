package ciaudit

import (
	"os"
	"path/filepath"
	"testing"
)

// gpCovMakeUnreadable turns path into an entry every reader in this package
// must report an error for. It uses a dangling symlink where the platform
// supports one (every unix) and falls back to permission bits otherwise.
func gpCovMakeUnreadable(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
	dangling := filepath.Join(filepath.Dir(path), "gpcov-absent-target")
	if err := os.Symlink(dangling, path); err == nil {
		return
	}
	// Platforms without symlink support (a locked-down Windows) still refuse a
	// read once the write bit is cleared, for an unprivileged process.
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("rewrite %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this platform exposes no way to make a regular file unreadable")
	}
}

// gpCovFinding returns the one finding with code, failing the test when the
// report does not carry it.
func gpCovFinding(t *testing.T, report Report, code string) Finding {
	t.Helper()
	for _, finding := range report.Findings {
		if finding.Code == code {
			return finding
		}
	}
	t.Fatalf("finding %q missing from %+v", code, report.Findings)
	return Finding{}
}

// gpCovFindings renders a report's findings as "code@file" pairs so a test can
// assert their exact order.
func gpCovFindings(findings []Finding) []string {
	rendered := make([]string, 0, len(findings))
	for _, finding := range findings {
		rendered = append(rendered, finding.Code+"@"+finding.File)
	}
	return rendered
}

func gpCovAssertFindings(t *testing.T, findings []Finding, want []string) {
	t.Helper()
	got := gpCovFindings(findings)
	if len(got) != len(want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("findings = %v, want %v", got, want)
		}
	}
}

// gpCovRepo builds a real Git repository whose main branch is pushed to a bare
// origin, so CompareCoverageFloors can fetch a target that is not the branch
// currently checked out. Callers branch off main themselves.
func gpCovRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	targetGit(t, root, "init", "-q", "-b", "main")
	targetGit(t, root, "config", "user.email", "audit@example.test")
	targetGit(t, root, "config", "user.name", "audit")
	if len(files) == 0 {
		files = map[string]string{"README.md": "gpCov fixture\n"}
	}
	for name, content := range files {
		write(t, root, name, content)
	}
	targetGit(t, root, "add", "-A")
	targetGit(t, root, "commit", "-qm", "init")

	remote := filepath.Join(t.TempDir(), "remote.git")
	targetGit(t, root, "init", "-q", "--bare", remote)
	targetGit(t, root, "remote", "add", "origin", remote)
	targetGit(t, root, "push", "-q", "origin", "main")
	return root
}
