//go:build e2e

package worktrees

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreejournal"
)

type parkedNextProof struct {
	projection, claim, events []byte
	head                      string
}

func parkedNextClaimPath(t *testing.T, projectsRoot, worktree string) string {
	t.Helper()
	projection, err := readWorkLogProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, "worklogs", projection.EffortID, "runs", projection.RunID, "claims", projection.ClaimID+".json")
}

func parkedNextEvidence(t *testing.T, projectsRoot, worktree string) parkedNextProof {
	t.Helper()
	projection, err := os.ReadFile(filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := os.ReadFile(parkedNextClaimPath(t, projectsRoot, worktree))
	if err != nil {
		t.Fatal(err)
	}
	events, err := os.ReadFile(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, worktreejournal.EventsName))
	if err != nil {
		t.Fatal(err)
	}
	return parkedNextProof{projection: projection, claim: claim, events: events, head: gitTestOutput(t, worktree, "rev-parse", "HEAD")}
}

func assertParkedNextEvidence(t *testing.T, projectsRoot, worktree string, before parkedNextProof) {
	t.Helper()
	after := parkedNextEvidence(t, projectsRoot, worktree)
	if !bytes.Equal(after.projection, before.projection) || !bytes.Equal(after.claim, before.claim) || !bytes.Equal(after.events, before.events) || after.head != before.head {
		t.Fatalf("refusal changed projection, immutable claim, local events, or HEAD: projection=%t claim=%t events=%t HEAD=%q to %q",
			bytes.Equal(after.projection, before.projection), bytes.Equal(after.claim, before.claim), bytes.Equal(after.events, before.events), before.head, after.head)
	}
}

func newParkedNextLocalFixture(t *testing.T, operation string) (*gitFixture, string, session.Record, sessionpark.Worktree, sessionpark.Bundle) {
	t.Helper()
	fixture, worktree, source := newSessionCheckpointFixture(t, operation)
	branch := preparePushedParkedWorktree(t, fixture, worktree)
	_, member := captureParkedWorktreeMember(t, fixture, worktree, source, branch)
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion,
		ParkedSessionID: "park-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", Source: source,
		Continuation: "private continuation", ParkedAt: time.Now().UTC(), Worktrees: []sessionpark.Worktree{member}}
	return fixture, worktree, source, member, bundle
}

//nolint:paralleltest // native Work Log fixture sets process-wide Git and agent environment
func TestE2EParkedLockedSnapshotRefusesChangedAuthorityWithoutPublishing(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, string, string, *os.File)
	}{
		{"inactive projection", "active managed Work Log", func(t *testing.T, _, worktree string, _ *os.File) {
			projection, err := readWorkLogProjection(worktree)
			if err != nil {
				t.Fatal(err)
			}
			projection.Lifecycle = "terminal"
			if err := writeWorkLogProjectionAt(worktree, projection); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt private claim", "corroborate managed Work Log", func(t *testing.T, projectsRoot, worktree string, _ *os.File) {
			if err := os.WriteFile(parkedNextClaimPath(t, projectsRoot, worktree), []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"malformed journal", "inspect exact parked owner event", func(t *testing.T, _, worktree string, _ *os.File) {
			path := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, worktreejournal.EventsName)
			if err := os.WriteFile(path, []byte("{malformed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"later different owner", "exact latest parked owner", func(t *testing.T, _, worktree string, journal *os.File) {
			events, _, err := readLocalEventsForAppend(journal)
			if err != nil {
				t.Fatal(err)
			}
			var latest LocalWorkLogEvent
			for _, event := range events {
				if event.Type == LocalEventOwner && event.Owner != nil {
					latest = event
				}
			}
			if latest.Owner == nil {
				t.Fatal("fixture has no source owner")
			}
			owner := *latest.Owner
			owner.PID = os.Getpid() + 1000000
			latest.At = time.Now().UTC()
			owner.At = latest.At
			latest.ID += "-later"
			latest.Owner = &owner
			if _, _, err := appendLocalEventUnderLock(worktree, journal, latest); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		//nolint:paralleltest // each child builds a native fixture using t.Setenv
		t.Run(tc.name, func(t *testing.T) {
			fixture, worktree, source := newSessionCheckpointFixture(t, "park-locked-"+strings.ReplaceAll(tc.name, " ", "-"))
			journal, err := openJournalSubdirectory(worktree, worklogDirectory, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = journal.Close() }()
			unlock, err := lockLocalWorkLog(journal)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			if _, ownerID, err := parkedSessionWorkLogSnapshotUnderLock(fixture.projectsRoot, worktree, source, journal); err != nil || ownerID == "" {
				t.Fatalf("valid locked source = owner %q, %v", ownerID, err)
			}
			tc.change(t, fixture.projectsRoot, worktree, journal)
			before := parkedNextEvidence(t, fixture.projectsRoot, worktree)
			_, _, err = parkedSessionWorkLogSnapshotUnderLock(fixture.projectsRoot, worktree, source, journal)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("wrong refusal boundary: %v, want %q", err, tc.want)
			}
			assertParkedNextEvidence(t, fixture.projectsRoot, worktree, before)
		})
	}
}

//nolint:paralleltest // native Git/Work Log fixtures set process-wide environment
func TestE2EParkedLocalAcquireRefusesIdentityJournalAndLockSubstitution(t *testing.T) {
	// The direct acquire fixture must admit and release an unchanged member;
	// otherwise a negative case could pass before reaching its mutation.
	//nolint:paralleltest // the native fixture uses t.Setenv
	t.Run("valid direct acquire releases custody", func(t *testing.T) {
		fixture, worktree, _, member, _ := newParkedNextLocalFixture(t, "park-acquire-control")
		custody := &ParkedLocalCustody{projectsRoot: fixture.projectsRoot, members: []parkedLocalMember{{member: member}}}
		if err := custody.acquire(context.Background(), 0); err != nil {
			t.Fatalf("valid direct acquire: %v", err)
		}
		if custody.members[0].worktree == nil || custody.members[0].directory == nil || custody.members[0].unlock == nil {
			t.Fatal("valid direct acquire omitted retained worktree, journal, or lock")
		}
		custody.close()
		assertParkedCaptureLockAvailable(t, worktree)
	})
	for _, tc := range []string{"wrong branch", "occupied journal", "symlinked lock"} {
		//nolint:paralleltest // each child builds a native fixture using t.Setenv
		t.Run(tc, func(t *testing.T) {
			fixture, worktree, _, member, _ := newParkedNextLocalFixture(t, "park-acquire-"+strings.ReplaceAll(tc, " ", "-"))
			journal := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory)
			before := parkedNextEvidence(t, fixture.projectsRoot, worktree)
			registration := gitTestOutput(t, member.CanonicalDir, "worktree", "list", "--porcelain")
			want := ""
			switch tc {
			case "wrong branch":
				member.Branch = "wb/another-parked-branch"
				want = "managed worktree identity changed since park"
			case "occupied journal":
				if err := os.Rename(journal, journal+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(journal, []byte("occupant"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "open parked member Work Log journal"
			case "symlinked lock":
				lock := filepath.Join(journal, worktreejournal.LockName)
				if err := os.Rename(lock, lock+".saved"); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(t.TempDir(), "outside-lock")
				if err := os.WriteFile(outside, []byte("protected"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, lock); err != nil {
					t.Fatal(err)
				}
				want = "open local work-log journal lock"
			}
			custody := &ParkedLocalCustody{projectsRoot: fixture.projectsRoot, members: []parkedLocalMember{{member: member}}}
			err := custody.acquire(context.Background(), 0)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("acquire refusal = %v, want %q", err, want)
			}
			if custody.members[0].worktree != nil || custody.members[0].directory != nil || custody.members[0].unlock != nil {
				t.Fatal("failed acquisition retained a handle or lock")
			}
			if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != before.head {
				t.Fatalf("failed acquisition moved HEAD: %q -> %q", before.head, got)
			}
			if got := gitTestOutput(t, member.CanonicalDir, "worktree", "list", "--porcelain"); got != registration {
				t.Fatalf("failed acquisition changed Git registration: before=%q after=%q", registration, got)
			}
			if tc == "occupied journal" {
				if raw, err := os.ReadFile(journal); err != nil || string(raw) != "occupant" {
					t.Fatalf("journal occupant = %q, %v", raw, err)
				}
				projection, err := os.ReadFile(filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName))
				if err != nil || !bytes.Equal(projection, before.projection) {
					t.Fatalf("projection changed: equal=%t err=%v", bytes.Equal(projection, before.projection), err)
				}
				claim, err := os.ReadFile(parkedNextClaimPath(t, fixture.projectsRoot, worktree))
				if err != nil || !bytes.Equal(claim, before.claim) {
					t.Fatalf("private claim changed: equal=%t err=%v", bytes.Equal(claim, before.claim), err)
				}
				if _, err := os.Stat(journal + ".saved"); err != nil {
					t.Fatalf("original journal lost: %v", err)
				}
				events, err := os.ReadFile(filepath.Join(journal+".saved", worktreejournal.EventsName))
				if err != nil || !bytes.Equal(events, before.events) {
					t.Fatalf("original journal events changed: equal=%t err=%v", bytes.Equal(events, before.events), err)
				}
			} else {
				assertParkedNextEvidence(t, fixture.projectsRoot, worktree, before)
			}
			if tc == "symlinked lock" {
				if target, err := os.Readlink(filepath.Join(journal, worktreejournal.LockName)); err != nil || target == "" {
					t.Fatalf("lock symlink lost: %q, %v", target, err)
				}
				if raw, err := os.ReadFile(filepath.Join(journal, worktreejournal.LockName)); err != nil || string(raw) != "protected" {
					t.Fatalf("outside lock changed: %q, %v", raw, err)
				}
				if _, err := os.Stat(filepath.Join(journal, worktreejournal.LockName+".saved")); err != nil {
					t.Fatalf("original lock lost: %v", err)
				}
			}
		})
	}
}

//nolint:paralleltest // each native fixture sets process-wide Git and agent environment
func TestE2EParkedLocalHeldValidationRefusesLaterGitClaimAndEventDrift(t *testing.T) {
	for _, tc := range []string{"closed descriptor", "changed HEAD", "changed projection", "corrupt private claim", "malformed events"} {
		//nolint:paralleltest // each child builds a native fixture using t.Setenv
		t.Run(tc, func(t *testing.T) {
			fixture, worktree, _, _, bundle := newParkedNextLocalFixture(t, "park-validate-"+strings.ReplaceAll(tc, " ", "-"))
			err := WithParkedLocalResumeCustody(context.Background(), fixture.projectsRoot, bundle, func(custody *ParkedLocalCustody) error {
				prepared := &custody.members[0]
				want := ""
				switch tc {
				case "closed descriptor":
					if err := prepared.worktree.worktree.Close(); err != nil {
						t.Fatal(err)
					}
					want = "retained worktree descriptor changed since park"
				case "changed HEAD":
					gitTest(t, worktree, "branch", "parked-changed")
					gitTest(t, worktree, "symbolic-ref", "HEAD", "refs/heads/parked-changed")
					want = "worktree branch or HEAD changed after park"
				case "changed projection":
					projection, err := readWorkLogProjection(worktree)
					if err != nil {
						t.Fatal(err)
					}
					projection.Lifecycle = "terminal"
					if err := writeWorkLogProjectionAt(worktree, projection); err != nil {
						t.Fatal(err)
					}
					want = "active Work Log claim changed after park"
				case "corrupt private claim":
					if err := os.WriteFile(parkedNextClaimPath(t, fixture.projectsRoot, worktree), []byte("{}\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					want = "invalid work-log effort/run identity"
				case "malformed events":
					path := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, worktreejournal.EventsName)
					if err := os.WriteFile(path, []byte("{malformed\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					want = "parse local work-log event"
				}
				before := parkedNextEvidence(t, fixture.projectsRoot, worktree)
				branchBefore := ""
				if tc == "changed HEAD" {
					branchBefore = gitTestOutput(t, worktree, "symbolic-ref", "--quiet", "--short", "HEAD")
				}
				failure := custody.validate(context.Background())
				if failure == nil || !strings.Contains(failure.Error(), want) {
					t.Fatalf("validation refusal = %v, want %q", failure, want)
				}
				assertParkedNextEvidence(t, fixture.projectsRoot, worktree, before)
				if branchBefore != "" {
					if branchAfter := gitTestOutput(t, worktree, "symbolic-ref", "--quiet", "--short", "HEAD"); branchAfter != branchBefore {
						t.Fatalf("refused validation moved branch: %q -> %q", branchBefore, branchAfter)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			assertParkedCaptureLockAvailable(t, worktree)
		})
	}
}
