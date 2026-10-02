//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // Native transfer fixture sets WB and Git process environment.
func TestE2EWorkLogProjectionNextLegacyAttestationRechecksNativeRaceWindows(t *testing.T) {
	for _, kind := range []string{"origin removed", "journal changes before reread", "journal changes before append", "foreign intent wins append", "private claim missing", "same repository"} {
		//nolint:paralleltest // Each case creates a native process-environment fixture.
		t.Run(kind, func(t *testing.T) {
			fixture := newRelocationLegacyProofFixture(t)
			run, path, err := openWorkLogRun(fixture.home, fixture.claim.EffortID, fixture.claim.RunID, false)
			if err != nil {
				t.Fatal(err)
			}
			directory, err := openPrivateChild(run, "relocations", true)
			if err != nil {
				t.Fatal(err)
			}
			if err := directory.Close(); err != nil {
				t.Fatal(err)
			}
			if err := run.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == "private claim missing" {
				if err := os.Remove(fixture.claimPath); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "same repository" {
				fixture.entry.Repository = fixture.claim.Repository
			}

			hit := false
			observe := func(phase workLogProjectionBoundary, _ *os.File) {
				want := workLogLegacyOriginCorroborated
				if kind == "journal changes before append" || kind == "foreign intent wins append" {
					want = workLogLegacyIntentBeforeAppend
				}
				if phase != want {
					return
				}
				hit = true
				switch kind {
				case "origin removed":
					gitTest(t, fixture.moved, "remote", "remove", "origin")
				case "journal changes before reread", "journal changes before append":
					projectionNextWrite(t, filepath.Join(path, "relocations", relocationIntentName(fixture.claim.ClaimID, "changed")), []byte("{bad"))
				case "foreign intent wins append":
					if _, _, err := appendRelocationIntentForRepository(fixture.home, fixture.claim, fixture.claim.Worktree, fixture.moved, workLogRelocationLegacyCheckout, fixture.entry.HeadSHA, fixture.claim.Repository, fixture.entry.Repository, "git@github.com:newco/renamed.git", relocationPlacementRecord{}, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
			}
			proved, err := legacyRepositoryRelocationForCleanupObserved(context.Background(), fixture.home, fixture.projectsRoot, fixture.entry, true, nil, observe)
			if kind == "private claim missing" || kind == "same repository" {
				if hit || proved {
					t.Fatalf("early private evidence refusal/no-op=%v %v observed=%v", proved, err, hit)
				}
				if kind == "private claim missing" && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing claim cause=%v", err)
				}
				if kind == "same repository" && err != nil {
					t.Fatalf("same repository no-op=%v", err)
				}
				if names, readErr := os.ReadDir(filepath.Join(path, "relocations")); readErr != nil || len(names) != 0 {
					t.Fatalf("early refusal/no-op published history=%v %v", names, readErr)
				}
				if got := gitTestOutput(t, fixture.moved, "rev-parse", "HEAD"); got != fixture.entry.HeadSHA {
					t.Fatalf("early refusal/no-op changed HEAD=%s", got)
				}
				return
			}

			if !hit || proved || err == nil {
				t.Fatalf("attestation race=%v %v hit=%v", proved, err, hit)
			}
			want := map[string]string{"origin removed": "origin is ambiguous", "journal changes before reread": "decode relocation journal", "journal changes before append": "append legacy checkout attestation intent", "foreign intent wins append": "durable legacy checkout attestation does not match"}[kind]
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("wrong attestation phase=%v want=%q", err, want)
			}
			names, readErr := os.ReadDir(filepath.Join(path, "relocations"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, name := range names {
				if strings.HasSuffix(name.Name(), ".completed.json") {
					t.Fatalf("refusal published completion %s", name.Name())
				}
			}
			if got := gitTestOutput(t, fixture.moved, "rev-parse", "HEAD"); got != fixture.entry.HeadSHA {
				t.Fatalf("immutable HEAD changed=%s", got)
			}
		})
	}
}
func TestE2EWorkLogProjectionNextPlacementRetainsObservedForgeAndLegacyHost(t *testing.T) {
	t.Parallel()
	projects := projectionNextTemp(t)
	canonical := filepath.Join(projects, "newco", "renamed")
	if err := os.MkdirAll(canonical, 0755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, canonical, "init")
	gitTest(t, canonical, "remote", "add", "origin", "git@gitlab.com:newco/renamed.git")
	store := projectionNextTemp(t)
	task := "transferred"
	entry := ListResult{Repository: "newco/renamed", CanonicalDir: canonical, WorktreesRoot: store, Task: task}
	for _, host := range []string{"gitlab.com", ""} {
		entry.WorktreeDir = filepath.Join(store, task, host, "newco", "renamed")
		claim := workLogClaim{Repository: "github.com/acme/app", Worktree: filepath.Join(store, task, host, "acme", "app")}
		if err := legacyRepositoryRelocationPaths(projects, entry, claim); err != nil {
			t.Fatalf("observed host %s refused=%v", host, err)
		}
	}
}
