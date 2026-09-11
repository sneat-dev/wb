package agentguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func agentCall(toolInputJSON, cwd string) ToolCall {
	return ToolCall{
		HookEventName: "PreToolUse",
		ToolName:      "Agent",
		CWD:           cwd,
		ToolInput:     json.RawMessage(toolInputJSON),
	}
}

func agentDispatch(prompt, model string) string {
	document := map[string]string{"prompt": prompt}
	if model != "" {
		document["model"] = model
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// TestAgentDispatchRequiresModel pins
// lesson:l49-subagent-dispatch-should-fail-before-an-unnamed-model-can-inherit:
// a dispatch that never names a model inherits the parent's silently, which
// is exactly the rule ("Name the model on every subagent dispatch — never
// let it inherit") a prose-only rule could not fail closed on.
func TestAgentDispatchRequiresModel(t *testing.T) {
	repositories := newFixture(t)

	t.Run("no model is denied", func(t *testing.T) {
		decision := Inspect(agentCall(agentDispatch("Investigate the failing test.", ""), repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
		if !decision.Deny {
			t.Fatal("Inspect allowed a dispatch with no model")
		}
		for _, expected := range []string{"model", "l49-subagent-dispatch-should-fail-before-an-unnamed-model-can-inherit", "Name the model on every subagent dispatch"} {
			if !strings.Contains(decision.Reason, expected) {
				t.Fatalf("refusal is missing %q:\n%s", expected, decision.Reason)
			}
		}
	})

	t.Run("an explicit model is allowed", func(t *testing.T) {
		decision := Inspect(agentCall(agentDispatch("Investigate the failing test.", "sonnet"), repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
		if decision.Deny {
			t.Fatalf("Inspect refused a dispatch that named a model:\n%s", decision.Reason)
		}
	})

	t.Run("an empty/unrecognised tool_input is allowed, not guessed at", func(t *testing.T) {
		decision := Inspect(agentCall(`{}`, repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
		if decision.Deny {
			t.Fatalf("Inspect refused an empty tool_input:\n%s", decision.Reason)
		}
	})
}

// TestAgentDispatchRefusesLiteralReportPath pins
// lesson:a-report-path-hand-written-into-a-brief-diverges-from-wb-home-on-the-target-host.
func TestAgentDispatchRefusesLiteralReportPath(t *testing.T) {
	repositories := newFixture(t)

	denied := []string{
		"When done, write your report to /Users/alex/.wb/reports/task.md",
		"Report at $HOME/.wb/reports/task.md when finished.",
		"Report at ~/.wb/reports/task.md when finished.",
	}
	for _, prompt := range denied {
		t.Run(prompt, func(t *testing.T) {
			decision := Inspect(agentCall(agentDispatch(prompt, "sonnet"), repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
			if !decision.Deny {
				t.Fatalf("Inspect(%q) allowed a literal report path", prompt)
			}
			for _, expected := range []string{"brief-report-path-from-owning-verb", "wb worktree log finalize --report"} {
				if !strings.Contains(decision.Reason, expected) {
					t.Fatalf("refusal is missing %q:\n%s", expected, decision.Reason)
				}
			}
		})
	}

	t.Run("deriving the path from the owning verb is allowed", func(t *testing.T) {
		prompt := "When done, finalize with `wb worktree log finalize --report` and report the printed path."
		decision := Inspect(agentCall(agentDispatch(prompt, "sonnet"), repositories.Canonical), Options{ProjectsRoot: repositories.ProjectsRoot})
		if decision.Deny {
			t.Fatalf("Inspect refused a brief that derived its report path:\n%s", decision.Reason)
		}
	})
}

// claimFixture builds a repo-local `.worktrees/<task>` layout — WB's default
// (see AGENTS.md §1) — with a `.wb/local/manifest.yaml` naming the claimed
// repository, and a local wb-state mirror holding the matching claim file.
type claimFixture struct {
	ProjectsRoot string
	Owner        string
	Repository   string
	Task         string
	WorktreePath string
}

func newClaimFixture(t *testing.T) claimFixture {
	t.Helper()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	owner, repository, task := "sneat-co", "backstage", "claimed-task-20260910"

	worktreePath := filepath.Join(projectsRoot, owner, repository, ".worktrees", task)
	if err := os.MkdirAll(filepath.Join(worktreePath, ".wb", "local"), 0o755); err != nil {
		t.Fatalf("create worktree layout: %v", err)
	}
	manifest := "version: 1\n" +
		"effort_id: " + task + "\n" +
		"repository: " + owner + "/" + repository + "\n" +
		"worktree: " + worktreePath + "\n" +
		"branch: " + task + "\n"
	if err := os.WriteFile(filepath.Join(worktreePath, ".wb", "local", "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	configHome := filepath.Join(root, "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	wbConfig := "remote:\n  provider: git\n  repo: " + owner + "/wb-state\n"
	if err := os.MkdirAll(filepath.Join(configHome, "wb"), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "wb", "wb.yaml"), []byte(wbConfig), 0o644); err != nil {
		t.Fatalf("write wb.yaml: %v", err)
	}

	claimsDir := filepath.Join(projectsRoot, owner, "wb-state", "claims")
	if err := os.MkdirAll(claimsDir, 0o755); err != nil {
		t.Fatalf("create claims dir: %v", err)
	}
	claim := "schema_version: 1\ntask: " + task + "\nlogin: someoneelse\nmachine: theirlaptop\n"
	if err := os.WriteFile(filepath.Join(claimsDir, task+".yaml"), []byte(claim), 0o644); err != nil {
		t.Fatalf("write claim: %v", err)
	}

	return claimFixture{
		ProjectsRoot: projectsRoot,
		Owner:        owner,
		Repository:   repository,
		Task:         task,
		WorktreePath: worktreePath,
	}
}

// TestAgentDispatchRefusesLiveClaim pins
// lesson:a-brief-was-dispatched-for-work-already-under-an-active-wb-claim: a
// second brief for a repository already claimed by another lane is denied,
// naming the claim's task and owner.
func TestAgentDispatchRefusesLiveClaim(t *testing.T) {
	claim := newClaimFixture(t)
	elsewhere := t.TempDir()

	prompt := "Fix the bug in /Users/alex/projects/" + claim.Owner + "/" + claim.Repository + " described above."
	decision := Inspect(agentCall(agentDispatch(prompt, "sonnet"), elsewhere), Options{ProjectsRoot: claim.ProjectsRoot})
	if !decision.Deny {
		t.Fatal("Inspect allowed a dispatch into a live claim")
	}
	for _, expected := range []string{"brief-consults-live-claims-before-dispatch", claim.Task, "someoneelse/theirlaptop"} {
		if !strings.Contains(decision.Reason, expected) {
			t.Fatalf("refusal is missing %q:\n%s", expected, decision.Reason)
		}
	}
}

// TestAgentDispatchRefusesLiveClaimOnARelocatedHost covers a projects root
// that is not /Users/... — e.g. /home/ai/projects on the VM (see lesson
// a-report-path-hand-written-into-a-brief-diverges-from-wb-home-on-the-target-host)
// — so this policy is not blind on exactly the host most likely to run it.
func TestAgentDispatchRefusesLiveClaimOnARelocatedHost(t *testing.T) {
	claim := newClaimFixture(t)
	elsewhere := t.TempDir()

	prompt := "Fix the bug in /home/ai/projects/" + claim.Owner + "/" + claim.Repository + " described above."
	decision := Inspect(agentCall(agentDispatch(prompt, "sonnet"), elsewhere), Options{ProjectsRoot: claim.ProjectsRoot})
	if !decision.Deny {
		t.Fatal("Inspect allowed a dispatch into a live claim on a non-/Users/ host layout")
	}
}

// TestAgentDispatchRefusesLiveClaimNamedBesideWorktreeCreate covers the
// brief's second detection shape: an `owner/repo` token on the same line as
// `wb worktree create`, rather than an absolute path.
func TestAgentDispatchRefusesLiveClaimNamedBesideWorktreeCreate(t *testing.T) {
	claim := newClaimFixture(t)
	elsewhere := t.TempDir()

	prompt := "Run: wb worktree create fresh-task " + claim.Owner + "/" + claim.Repository
	decision := Inspect(agentCall(agentDispatch(prompt, "sonnet"), elsewhere), Options{ProjectsRoot: claim.ProjectsRoot})
	if !decision.Deny {
		t.Fatal("Inspect allowed a dispatch naming a claimed repo beside wb worktree create")
	}
	if !strings.Contains(decision.Reason, claim.Task) {
		t.Fatalf("refusal does not name the claimed task:\n%s", decision.Reason)
	}
}

// TestAgentDispatchLiveClaimFalsePositives pins the shapes this policy must
// not refuse: the same lane continuing its own work, an unclaimed
// repository, a released claim, and a prompt naming no repository at all.
func TestAgentDispatchLiveClaimFalsePositives(t *testing.T) {
	t.Run("dispatch from inside the claimed worktree is the same lane continuing", func(t *testing.T) {
		claim := newClaimFixture(t)
		prompt := "Continue the fix in /Users/alex/projects/" + claim.Owner + "/" + claim.Repository + "."
		decision := Inspect(agentCall(agentDispatch(prompt, "sonnet"), claim.WorktreePath), Options{ProjectsRoot: claim.ProjectsRoot})
		if decision.Deny {
			t.Fatalf("Inspect refused a dispatch from inside its own claimed worktree:\n%s", decision.Reason)
		}
	})

	t.Run("no repository named in the prompt", func(t *testing.T) {
		claim := newClaimFixture(t)
		decision := Inspect(agentCall(agentDispatch("Investigate the failing test.", "sonnet"), t.TempDir()), Options{ProjectsRoot: claim.ProjectsRoot})
		if decision.Deny {
			t.Fatalf("Inspect refused a dispatch naming no repository:\n%s", decision.Reason)
		}
	})

	t.Run("a released claim (claim file deleted) is not live", func(t *testing.T) {
		claim := newClaimFixture(t)
		if err := os.Remove(filepath.Join(claim.ProjectsRoot, claim.Owner, "wb-state", "claims", claim.Task+".yaml")); err != nil {
			t.Fatal(err)
		}
		prompt := "Fix the bug in /Users/alex/projects/" + claim.Owner + "/" + claim.Repository + "."
		decision := Inspect(agentCall(agentDispatch(prompt, "sonnet"), t.TempDir()), Options{ProjectsRoot: claim.ProjectsRoot})
		if decision.Deny {
			t.Fatalf("Inspect refused a dispatch against a released claim:\n%s", decision.Reason)
		}
	})

	t.Run("a repository with no worktrees at all", func(t *testing.T) {
		claim := newClaimFixture(t)
		prompt := "Fix the bug in /Users/alex/projects/" + claim.Owner + "/some-other-repo."
		decision := Inspect(agentCall(agentDispatch(prompt, "sonnet"), t.TempDir()), Options{ProjectsRoot: claim.ProjectsRoot})
		if decision.Deny {
			t.Fatalf("Inspect refused a dispatch against an unclaimed repository:\n%s", decision.Reason)
		}
	})

	t.Run("a hub remote provider has no local mirror to read", func(t *testing.T) {
		claim := newClaimFixture(t)
		configHome := os.Getenv("XDG_CONFIG_HOME")
		hubConfig := "remote:\n  provider: hub\n  url: https://wb-github-app.sneat.dev\n"
		if err := os.WriteFile(filepath.Join(configHome, "wb", "wb.yaml"), []byte(hubConfig), 0o644); err != nil {
			t.Fatal(err)
		}
		prompt := "Fix the bug in /Users/alex/projects/" + claim.Owner + "/" + claim.Repository + "."
		decision := Inspect(agentCall(agentDispatch(prompt, "sonnet"), t.TempDir()), Options{ProjectsRoot: claim.ProjectsRoot})
		if decision.Deny {
			t.Fatalf("Inspect refused a dispatch under a hub remote provider:\n%s", decision.Reason)
		}
	})
}
