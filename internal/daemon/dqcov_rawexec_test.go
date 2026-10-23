package daemon

import (
	"os"
	"path/filepath"

	"strings"
	"testing"
)

func TestDqCovRawExecutionPolicyPathIsPerUserAndOutsideProjects(t *testing.T) {
	t.Parallel()
	path, err := RawExecutionPolicyPath()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("policy path %q is not absolute", path)
	}
	if !strings.HasSuffix(filepath.ToSlash(path), "/.config/wb/daemon-raw-exec.json") {
		t.Fatalf("policy path = %q, want the per-user .config/wb location", path)
	}
}

// TestDqCovLoadRawExecutionPolicyWithDefaultPath covers the production call
// shape where no explicit path is supplied: it must resolve the per-user policy
// and still fail closed while that file is absent.
func TestDqCovLoadRawExecutionPolicyWithDefaultPath(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	defaultPath, err := RawExecutionPolicyPath()
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := LoadRawExecutionPolicy("", projectsRoot)
	if err != nil {
		t.Fatalf("LoadRawExecutionPolicy(\"\") = %v", err)
	}
	if _, statErr := os.Lstat(defaultPath); os.IsNotExist(statErr) && allowed {
		t.Fatalf("raw execution allowed without a policy file at %s", defaultPath)
	}
}

func TestDqCovLoadRawExecutionPolicySurfacesUnresolvableAndUninspectablePaths(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()

	t.Run("policy directory does not exist", func(t *testing.T) {
		t.Parallel()
		allowed, err := LoadRawExecutionPolicy(filepath.Join(t.TempDir(), "absent", "policy.json"), projectsRoot)
		if err != nil || allowed {
			t.Fatalf("absent policy directory = %t, %v", allowed, err)
		}
	})

	t.Run("projects root cannot be resolved", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "policy.json")
		writeRawExecutionPolicy(t, path, []byte(enabledRawExecutionPolicy), 0o600)
		allowed, err := LoadRawExecutionPolicy(path, filepath.Join(t.TempDir(), "absent-root"))
		if err == nil || allowed {
			t.Fatalf("unresolvable projects root = %t, %v", allowed, err)
		}
	})

	t.Run("policy path below a regular file", func(t *testing.T) {
		t.Parallel()
		file := filepath.Join(t.TempDir(), "regular-file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		allowed, err := LoadRawExecutionPolicy(filepath.Join(file, "policy.json"), projectsRoot)
		if err == nil || allowed || !strings.Contains(err.Error(), "inspect daemon raw-execution policy") {
			t.Fatalf("policy below a file = %t, %v", allowed, err)
		}
	})

	t.Run("policy directory below a regular file", func(t *testing.T) {
		t.Parallel()
		file := filepath.Join(t.TempDir(), "regular-file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		allowed, err := LoadRawExecutionPolicy(filepath.Join(file, "nested", "policy.json"), projectsRoot)
		if err == nil || allowed || !strings.Contains(err.Error(), "resolve daemon raw-execution policy directory") {
			t.Fatalf("policy directory below a file = %t, %v", allowed, err)
		}
	})
}

func TestDqCovLoadRawExecutionPolicyRejectsTrailingAndUnknownContent(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	externalRoot := t.TempDir()
	document := `{"version":1,"allow_raw_daemon_execution":true}`

	checks := []struct {
		name     string
		contents string
		want     string
	}{
		{"multiple values", document + document, "multiple JSON values"},
		{"trailing content", document + " ###", "trailing content"},
		{"unknown field", `{"version":1,"allow_raw_daemon_execution":true,"extra":1}`, "decode daemon raw-execution policy"},
		{"unsupported version", `{"version":2,"allow_raw_daemon_execution":true}`, "unsupported daemon raw-execution policy version 2"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(externalRoot, check.name+".json")
			writeRawExecutionPolicy(t, path, []byte(check.contents), 0o600)
			allowed, err := LoadRawExecutionPolicy(path, projectsRoot)
			if err == nil || allowed || !strings.Contains(err.Error(), check.want) {
				t.Fatalf("policy %q = %t, %v, want %q", check.contents, allowed, err, check.want)
			}
			if requireErr := RequireRawExecutionPolicy(path, projectsRoot); requireErr == nil {
				t.Fatal("RequireRawExecutionPolicy accepted a malformed policy")
			}
		})
	}
}

func TestDqCovPathWithinComparesResolvedAbsolutePaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(resolvedRoot, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	sibling := resolvedRoot + "-sibling"
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(sibling) })

	checks := []struct {
		name      string
		candidate string
		want      bool
	}{
		{"root itself", resolvedRoot, true},
		{"nested path", nested, true},
		{"sibling sharing a name prefix", sibling, false},
		{"parent", filepath.Dir(resolvedRoot), false},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			t.Parallel()
			got, err := pathWithin(root, check.candidate)
			if err != nil {
				t.Fatal(err)
			}
			if got != check.want {
				t.Fatalf("pathWithin(%q, %q) = %t, want %t", root, check.candidate, got, check.want)
			}
		})
	}

	if _, err := pathWithin(filepath.Join(t.TempDir(), "absent"), root); err == nil {
		t.Fatal("pathWithin accepted an unresolvable root")
	}
}
