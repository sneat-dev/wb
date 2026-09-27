package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLifecycleCheckTextDistinguishesMissingAndTrustedConfiguration(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing.yaml")
	for _, test := range []struct {
		name, config, want string
	}{
		{"missing", missing, "Lifecycle hooks not configured"},
		{"trusted", lifecycleTestConfig(t, root, lifecycleTestExecutable(t, root)), "code-index"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := newHooksLifecycleCheckCmd()
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetArgs([]string{"--config", test.config})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("check output %q lacks %q", output.String(), test.want)
			}
		})
	}
}

func TestLifecycleBackfillTextNamesPlannedExecutionAndPreviewAction(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	initOriginRepository(t, filepath.Join(projects, "acme", "app"), "acme/app")
	configHome := filepath.Join(root, "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	executable := lifecycleTestExecutable(t, root)
	config := filepath.Join(configHome, "wb", "wb.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(lifecycleConfigContents(executable)), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	args := []string{"hooks", "lifecycle", "backfill", "--projects-root", projects}
	if code := run(args, &out, &diagnostic); code != exitOK {
		t.Fatalf("run(%q) = %d: %s", args, code, diagnostic.String())
	}
	for _, want := range []string{"planned 1 execution(s) from 1 repositories", "github.com/acme/app@", "Re-run with --apply"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("backfill output %q lacks %q", out.String(), want)
		}
	}
}

func TestLifecycleGCTextReportsEmptyPreview(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	cmd := newHooksLifecycleGCCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would remove 0 of 0 receipts") {
		t.Fatalf("GC preview = %q", out.String())
	}
}
