package agentguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tagRepoFixture is a canonical clone this guard would classify as managed,
// with room to add `.wb/hooks.yaml` and `.github/workflows/*.yml` per test
// case.
type tagRepoFixture struct {
	ProjectsRoot string
	Repo         string
}

func newTagRepoFixture(t *testing.T) tagRepoFixture {
	t.Helper()
	root := t.TempDir()
	// Isolate the global hooks policy lookup from whatever the real machine
	// running this test happens to have in ~/.config/wb/wb.yaml — reading
	// that live file here would make the test's outcome depend on the
	// operator's own config instead of the fixture.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	projectsRoot := filepath.Join(root, "projects")
	repo := filepath.Join(projectsRoot, "dal-go", "dalgo2example")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	command := exec.Command("git", "-C", repo, "init", "-q", "-b", "main")
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	return tagRepoFixture{ProjectsRoot: projectsRoot, Repo: repo}
}

func (f tagRepoFixture) writeHooksConfig(t *testing.T, autoTags bool) {
	t.Helper()
	value := "false"
	if autoTags {
		value = "true"
	}
	content := "version: 1\nagent:\n  autoTags: " + value + "\n"
	if err := os.MkdirAll(filepath.Join(f.Repo, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Repo, ".wb", "hooks.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f tagRepoFixture) writeWorkflow(t *testing.T, name, content string) {
	t.Helper()
	dir := filepath.Join(f.Repo, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const autoTaggingWorkflow = `name: Go CI
on:
  push:
    branches: [main]
jobs:
  strongo_workflow:
    uses: strongo/cicd/.github/workflows/workflow.yml@main
    with:
      code_coverage: true
`

const optedOutWorkflow = `name: Go CI
on:
  push:
    branches: [main]
jobs:
  strongo_workflow:
    uses: strongo/cicd/.github/workflows/workflow.yml@main
    with:
      disable-version-bumping: true
`

// TestGitTagDeniedInAutoTaggingRepository pins lesson l3/l11: hand-tagging a
// repository whose own CI already tags it can hand-tag a lower version onto
// newer code, hiding the fix.
func TestGitTagDeniedInAutoTaggingRepository(t *testing.T) {
	t.Run("explicit .wb/hooks.yaml agent.autoTags: true", func(t *testing.T) {
		repo := newTagRepoFixture(t)
		repo.writeHooksConfig(t, true)
		decision := Inspect(bashCall("git tag v1.2.3", repo.Repo), Options{ProjectsRoot: repo.ProjectsRoot})
		if !decision.Deny {
			t.Fatal("Inspect allowed a hand tag in a repo declaring agent.autoTags: true")
		}
		for _, expected := range []string{"l3-check-existing-tags", "l11-find-out-whether-the-repo-auto-tags", "autoTags: true"} {
			if !strings.Contains(decision.Reason, expected) {
				t.Fatalf("refusal missing %q:\n%s", expected, decision.Reason)
			}
		}
	})

	t.Run("workflow heuristic: strongo/cicd with no disable-version-bumping", func(t *testing.T) {
		repo := newTagRepoFixture(t)
		repo.writeWorkflow(t, "ci.yml", autoTaggingWorkflow)
		decision := Inspect(bashCall("git tag v1.2.3", repo.Repo), Options{ProjectsRoot: repo.ProjectsRoot})
		if !decision.Deny {
			t.Fatal("Inspect allowed a hand tag where the workflow heuristic should have fired")
		}
		if !strings.Contains(decision.Reason, "strongo/cicd reusable workflow") {
			t.Fatalf("refusal does not name the heuristic:\n%s", decision.Reason)
		}
	})

	t.Run("git push --tags is denied the same way", func(t *testing.T) {
		repo := newTagRepoFixture(t)
		repo.writeHooksConfig(t, true)
		decision := Inspect(bashCall("git push --tags", repo.Repo), Options{ProjectsRoot: repo.ProjectsRoot})
		if !decision.Deny {
			t.Fatal("Inspect allowed git push --tags in an auto-tagging repo")
		}
	})

	t.Run("git push origin <tag> is denied the same way", func(t *testing.T) {
		repo := newTagRepoFixture(t)
		repo.writeHooksConfig(t, true)
		decision := Inspect(bashCall("git push origin v1.2.3", repo.Repo), Options{ProjectsRoot: repo.ProjectsRoot})
		if !decision.Deny {
			t.Fatal("Inspect allowed git push origin <tag> in an auto-tagging repo")
		}
	})
}

// TestGitTagAutoTaggingFalsePositives pins the shapes that must stay allowed:
// no config and no heuristic hit, an explicit opt-out, read-only tag
// inspection, and an ordinary (non-tag) push.
func TestGitTagAutoTaggingFalsePositives(t *testing.T) {
	cases := []struct {
		name    string
		command string
		setup   func(t *testing.T, repo tagRepoFixture)
	}{
		{"no config and no heuristic hit", "git tag v1.2.3", func(t *testing.T, repo tagRepoFixture) {}},
		{"disable-version-bumping opts the repo out", "git tag v1.2.3", func(t *testing.T, repo tagRepoFixture) {
			repo.writeWorkflow(t, "ci.yml", optedOutWorkflow)
		}},
		{"explicit agent.autoTags: false", "git tag v1.2.3", func(t *testing.T, repo tagRepoFixture) {
			repo.writeHooksConfig(t, false)
		}},
		{"listing tags is read-only", "git tag -l", func(t *testing.T, repo tagRepoFixture) {
			repo.writeHooksConfig(t, true)
		}},
		{"deleting a tag is not hand-tagging a lower version", "git tag -d v0.0.1", func(t *testing.T, repo tagRepoFixture) {
			repo.writeHooksConfig(t, true)
		}},
		{"an ordinary push is not a tag push", "git push origin main", func(t *testing.T, repo tagRepoFixture) {
			repo.writeHooksConfig(t, true)
		}},
		{"a workflow that merely mentions strongo/cicd in a comment", "git tag v1.2.3", func(t *testing.T, repo tagRepoFixture) {
			repo.writeWorkflow(t, "ci.yml", "# see strongo/cicd docs\nname: CI\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go build ./...\n")
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := newTagRepoFixture(t)
			testCase.setup(t, repo)
			decision := Inspect(bashCall(testCase.command, repo.Repo), Options{ProjectsRoot: repo.ProjectsRoot})
			if decision.Deny {
				t.Fatalf("Inspect(%q) refused a legitimate call:\n%s", testCase.command, decision.Reason)
			}
		})
	}
}
