package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
)

// Cleanup must refuse to remove a worktree that an open stream still records
// as a local-link SOURCE: some consumer resolves it locally instead of a
// published version, and deleting it would strand that consumer exactly the
// way the 2026-09-07 incident did (a provider worktree removed while a live
// stream's go.work still named it). The guard fires for both link
// mechanisms `wb deps propagate local` records.
func TestCleanupRefusesAWorktreeStillLinkedAsASource(t *testing.T) {
	for _, mechanism := range []streams.Mechanism{streams.MechanismPnpmLink, streams.MechanismGoWork} {
		mechanism := mechanism
		t.Run(string(mechanism), func(t *testing.T) {
			task := "cleanup-linked-source-" + string(mechanism)
			fixture, result, head, mergedAt := prepareMergedTask(t, task)
			installMergedPullRequestFixture(t, head, mergedAt)
			now := mergedAt.Add(48 * time.Hour)

			store := streams.OpenAt(filepath.Join(fixture.home, "streams"))
			if _, err := store.Create(streams.Stream{
				Name: "consumer-stream-" + string(mechanism),
				Members: []streams.Member{{
					Repository: "acme/consumer", Worktree: "/work/consumer",
					Role: streams.RoleConsumer,
					Links: []streams.Link{{
						Library: result.WorktreeDir, LibraryRepository: result.Repository,
						Mechanism: mechanism, Identity: "acme/library",
					}},
				}},
			}); err != nil {
				t.Fatal(err)
			}

			outcome, err := Cleanup(context.Background(), CleanupOptions{
				ProjectsRoot: fixture.projectsRoot,
				Task:         task,
				Base:         "main",
				Apply:        true,
				DeleteRemote: true,
				OlderThan:    24 * time.Hour,
				Now:          func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(outcome.Results) != 1 || outcome.Results[0].Eligible || outcome.Results[0].Applied {
				t.Fatalf("outcome = %#v, want a refused, unapplied candidate", outcome.Results)
			}
			reason := outcome.Results[0].Reason
			if !strings.Contains(reason, "consumer-stream-"+string(mechanism)) {
				t.Errorf("refusal does not name the stream: %s", reason)
			}
			if !strings.Contains(reason, "acme/consumer") {
				t.Errorf("refusal does not name the consumer: %s", reason)
			}
			if !strings.Contains(reason, "wb deps propagate local "+result.WorktreeDir+" --to /work/consumer --undo") {
				t.Errorf("refusal does not name the exact repoint command: %s", reason)
			}
			if _, statErr := os.Stat(result.WorktreeDir); statErr != nil {
				t.Fatalf("refused worktree was removed: %v", statErr)
			}
		})
	}
}

// A worktree no open stream links to proceeds exactly as it always has.
func TestCleanupProceedsWhenNoStreamLinksTheWorktree(t *testing.T) {
	task := "cleanup-unlinked-source"
	fixture, result, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	now := mergedAt.Add(48 * time.Hour)

	// An unrelated stream, linking to a different worktree entirely, must not
	// influence this candidate's eligibility.
	store := streams.OpenAt(filepath.Join(fixture.home, "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "unrelated-stream",
		Members: []streams.Member{{
			Repository: "acme/other-consumer", Worktree: "/work/other-consumer",
			Role: streams.RoleConsumer,
			Links: []streams.Link{{
				Library: "/some/other/worktree", LibraryRepository: "acme/other-library",
				Mechanism: streams.MechanismGoWork, Identity: "acme/other-library",
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	relocatedRemote := filepath.Join(fixture.canonical, ".wb-test-remote.git")
	if err := os.Rename(fixture.remote, relocatedRemote); err != nil {
		t.Fatalf("relocate fixture remote under an authorized cleanup write root: %v", err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", relocatedRemote)

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         task,
		Base:         "main",
		Apply:        true,
		DeleteRemote: true,
		OlderThan:    24 * time.Hour,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Eligible || !outcome.Results[0].Applied {
		t.Fatalf("outcome = %#v, want the unlinked candidate to apply normally", outcome.Results)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("worktree still exists after cleanup: %v", statErr)
	}
}

// A stream that has ended released its repositories; its recorded links no
// longer describe anything live and must not block removal.
func TestCleanupProceedsWhenTheLinkingStreamIsClosed(t *testing.T) {
	task := "cleanup-closed-stream-source"
	fixture, result, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	now := mergedAt.Add(48 * time.Hour)

	ended := mergedAt.Add(time.Hour)
	store := streams.OpenAt(filepath.Join(fixture.home, "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "ended-consumer-stream", EndedAt: &ended,
		Members: []streams.Member{{
			Repository: "acme/consumer", Worktree: "/work/consumer",
			Role: streams.RoleConsumer,
			Links: []streams.Link{{
				Library: result.WorktreeDir, LibraryRepository: result.Repository,
				Mechanism: streams.MechanismPnpmLink, Identity: "acme/library",
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	relocatedRemote := filepath.Join(fixture.canonical, ".wb-test-remote.git")
	if err := os.Rename(fixture.remote, relocatedRemote); err != nil {
		t.Fatalf("relocate fixture remote under an authorized cleanup write root: %v", err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", relocatedRemote)

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         task,
		Base:         "main",
		Apply:        true,
		DeleteRemote: true,
		OlderThan:    24 * time.Hour,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Eligible || !outcome.Results[0].Applied {
		t.Fatalf("outcome = %#v, want a closed stream's stale link to be ignored", outcome.Results)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("worktree still exists after cleanup: %v", statErr)
	}
}
