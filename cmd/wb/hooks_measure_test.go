package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Actual root dispatch must not initialize runtime storage for read-only reports.
func TestHookReportsDoNotCreateRuntimeState(t *testing.T) {
	repo := t.TempDir()
	command := exec.Command("git", "init", "-b", "main")
	command.Dir = repo
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	configHome, stateHome := t.TempDir(), t.TempDir()
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("WB_PROJECTS_ROOT", projectsRoot)
	for _, name := range []string{"metrics", "measure"} {
		t.Run(name, func(t *testing.T) {
			args := []string{"hooks", name, repo, "--json"}
			if name == "measure" {
				args = append(args, "--projects-root", projectsRoot)
			}
			var out, diagnostic bytes.Buffer
			if code := run(args, &out, &diagnostic); code != exitOK {
				t.Fatalf("report exit %d: %s", code, diagnostic.String())
			}
			for _, path := range []string{filepath.Join(projectsRoot, ".wb"), filepath.Join(repo, ".wb"), filepath.Join(stateHome, "wb"), filepath.Join(configHome, "wb")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("read-only report created state %s: %v", path, err)
				}
			}
		})
	}
	var out, diagnostic bytes.Buffer
	if code := run([]string{"hooks", "metrics", repo, "--projects-root", projectsRoot}, &out, &diagnostic); code != exitUsage {
		t.Fatalf("metrics projects-root refusal changed: code=%d %s", code, diagnostic.String())
	}
}
