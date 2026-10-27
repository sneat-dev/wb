package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // native fixture scopes process-wide Git/WB settings.
func TestRelocationClaimHomesRetainCustodyAndRejectAmbiguousOrCorruptEvidence(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "home-proof", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	path := created[0].WorktreeDir
	projection, err := readWorkLogProjectionForReadOnlyClaim(path)
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	resolution := wbhome.Resolution{Write: wbhome.Layout{Home: empty}, Read: []wbhome.Layout{{Home: fixture.home}, {Home: fixture.home}}}
	claim, terminal, home, err := claimForRelocationAcrossHomes(resolution, path)
	if err != nil || terminal != nil || home != fixture.home || claim.Task != "home-proof" {
		t.Fatalf("legacy custody lost: claim=%+v terminal=%+v home=%s err=%v", claim, terminal, home, err)
	}
	if err := corroborateRecordedRepositoryTransfer(resolution, path); err != nil {
		t.Fatalf("guard legacy custody: %v", err)
	}
	original := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "claims", projection.ClaimID+".json")
	bytes, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"duplicate", "corrupt", "blocked-run"} {
		//nolint:paralleltest // subcases share process-wide Git/WB fixture state.
		t.Run(kind, func(t *testing.T) {
			other := t.TempDir()
			run := filepath.Join(other, "worklogs", projection.EffortID, "runs", projection.RunID)
			target := filepath.Join(run, "claims", projection.ClaimID+".json")
			if kind == "blocked-run" {
				if err := os.MkdirAll(filepath.Dir(run), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(run, []byte("obstruction"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				content := bytes
				if kind == "corrupt" {
					content = []byte("{")
				}
				if err := os.WriteFile(target, content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			homes := wbhome.Resolution{Write: wbhome.Layout{Home: other}, Read: []wbhome.Layout{{Home: fixture.home}}}
			_, _, _, readErr := claimForRelocationAcrossHomes(homes, path)
			guardErr := corroborateRecordedRepositoryTransfer(homes, path)
			if readErr == nil || guardErr == nil {
				t.Fatalf("%s hidden by later valid home: read=%v guard=%v", kind, readErr, guardErr)
			}
			if kind == "duplicate" && (!strings.Contains(readErr.Error(), "more than one") || !strings.Contains(guardErr.Error(), "more than one")) {
				t.Fatalf("duplicate authority not explicit: %v %v", readErr, guardErr)
			}
			if kind == "blocked-run" && (!errors.Is(readErr, syscall.ENOTDIR) || !errors.Is(guardErr, syscall.ENOTDIR)) {
				t.Fatalf("typed obstruction lost: %v %v", readErr, guardErr)
			}
			after, err := os.ReadFile(original)
			if err != nil || string(after) != string(bytes) {
				t.Fatalf("immutable proof changed: %v", err)
			}
		})
	}
	absent := wbhome.Resolution{Write: wbhome.Layout{Home: t.TempDir()}}
	_, _, _, err = claimForRelocationAcrossHomes(absent, path)
	if !errors.Is(err, errRelocationClaimAbsent) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("true absence lost typed boundary: %v", err)
	}
}
