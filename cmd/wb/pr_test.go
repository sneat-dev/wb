package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/streams"
)

// The hermetic API identifies the real producing branch. Its log and state
// expose attempted mutations even if the source guard is accidentally omitted.
func newPRLandPreflightFixture(t *testing.T) (*prCreateLinkPreflightFixture, string) {
	t.Helper()
	fixture := newPRCreateLinkPreflightFixture(t)
	fixture.writeState(t, "next-number", "1")
	worktree := fixture.createWorktree(t, "pr-land-source", "feature/pr-land-source")
	runCLIWorktreeGit(t, worktree, "push", "origin", "HEAD:refs/heads/feature/pr-land-source")
	t.Setenv("WB_PROJECTS_ROOT", fixture.projects)
	return fixture, worktree
}

func assertPRLandOnlyReadEvidence(t *testing.T, fixture *prCreateLinkPreflightFixture, number string) {
	t.Helper()
	log := fixture.ghLog(t)
	if log == "" {
		t.Fatal("authoritative PR head was not inspected")
	}
	for _, call := range strings.Split(strings.TrimSpace(log), "\n") {
		expected := "api repos/acme/app/pulls/" + number
		readOnly := false
		// The target's active rules are read to learn whether it requires a
		// merge queue, which decides the landing route.
		for _, endpoint := range []string{expected, expected + "/files?per_page=100", expected + "/commits?per_page=100", "api repos/acme/app/rules/branches/main?per_page=100"} {
			readOnly = readOnly || call == endpoint || call == endpoint+" --include"
		}
		if !readOnly {
			t.Fatalf("landing attempted an operation beyond read-only PR evidence: %s\n%s", call, log)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.state, "auto-merge")); !os.IsNotExist(err) {
		t.Fatalf("auto-merge was armed or evidence unavailable: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(fixture.state, "merged")); err != nil || string(raw) != "false" {
		t.Fatalf("merged = %q: %v", raw, err)
	}
}

//nolint:paralleltest // The hermetic API fixture selects a fake gh through process-wide PATH.
func TestPRLandInspectsAuthoritativeSourceBeforeLocalLinkPreflight(t *testing.T) {
	for _, keep := range []bool{false, true} {
		for _, selector := range []string{"acme/app#7", "https://github.com/acme/app/pull/7"} {
			t.Run(selector+"/keep="+strconv.FormatBool(keep), func(t *testing.T) { //nolint:paralleltest // Each fixture changes process-wide PATH and WB_PROJECTS_ROOT.
				fixture, worktree := newPRLandPreflightFixture(t)
				fixture.writeState(t, "next-number", "7")
				store := streams.OpenAt(filepath.Join(fixture.projects, ".wb", "streams"))
				if _, err := store.Create(streams.Stream{
					Name: "linked", Phase: streams.PhaseOpen,
					Members: []streams.Member{{Repository: "acme/app", Role: streams.RoleConsumer, Worktree: worktree,
						Links: []streams.Link{{Library: "/work/library", LibraryRepository: "acme/library", Mechanism: streams.MechanismGoWork, Identity: "github.com/acme/library/backend"}},
					}},
				}); err != nil {
					t.Fatal(err)
				}
				command := prCommandForTest(&invocation{projectsRoot: fixture.projects}, "land")
				command.SilenceUsage = true
				command.SilenceErrors = true
				var stdout, stderr bytes.Buffer
				command.SetOut(&stdout)
				command.SetErr(&stderr)
				args := []string{selector, "--non-interactive", "--format", "json"}
				if keep {
					args = append(args, "--keep")
				}
				command.SetArgs(args)
				if err := command.Execute(); err == nil {
					t.Fatal("linked source unexpectedly landed")
				}
				var result orchestrate.PullRequestLandResult
				if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
					t.Fatalf("receipt: %v\n%s\n%s", err, stdout.String(), stderr.String())
				}
				if result.RefusalCode != "cleanup-blocked-live-link" || !strings.Contains(result.Reason, worktree) || result.HeadRef != "feature/pr-land-source" || result.HeadSHA == "" || result.PullRequest != 7 || result.AutoMergeArmed {
					t.Fatalf("source refusal = %+v", result)
				}
				assertPRLandOnlyReadEvidence(t, fixture, "7")
			})
		}
	}
}
