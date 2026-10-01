package streams

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartPreservesEndedEvidenceWhenArchiveSourceDisappears(t *testing.T) {
	t.Parallel()
	engine, _, _, worktrees := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{".github/workflows/ci.yml": cancellingWorkflow})
	if _, err := engine.Store.Create(Stream{Name: "archive-race", Phase: PhaseEnded}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(engine.Store.statePath("archive-race"))
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "evidence")
	engine.Store.Now = func() time.Time {
		if err := os.Rename(engine.Store.Dir("archive-race"), backup); err != nil {
			t.Fatal(err)
		}
		return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	_, err = engine.Start(context.Background(), StartOptions{Name: "archive-race", Repositories: []string{"acme/library"}}, nil)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error=%v", err)
	}
	after, err := os.ReadFile(filepath.Join(backup, "stream.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("evidence=%q error=%v", after, err)
	}
	if len(worktrees.created) != 0 {
		t.Fatal("created worktrees after archive failed")
	}
}

func TestStartDoesNotReportOpenWhenFinalStateEncodingFails(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{".github/workflows/ci.yml": cancellingWorkflow})
	engine.Store.Now = func() time.Time {
		stream, err := engine.Store.Load("final-save")
		if err == nil && len(stream.Members) > 0 && stream.Members[0].PullRequest > 0 {
			return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	result, err := engine.Start(context.Background(), StartOptions{Name: "final-save", Repositories: []string{"acme/library"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "year outside of range") {
		t.Fatalf("error=%v", err)
	}
	saved, loadErr := engine.Store.Load("final-save")
	if loadErr != nil || saved.Phase != PhaseCreating || saved.Members[0].PullRequest == 0 || result.Stream.Phase != PhaseCreating {
		t.Fatalf("saved=%+v result=%+v error=%v", saved, result, loadErr)
	}
}

func TestJoinReportsStateLossAfterPublishingWork(t *testing.T) {
	t.Parallel()
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new member", true: "existing member"}[existing], func(t *testing.T) {
			t.Parallel()
			engine, _, _, worktrees := newTestEngine(t)
			for _, repo := range []string{"acme/library", "acme/app"} {
				writeCanonical(t, engine.ProjectsRoot, repo, map[string]string{".github/workflows/ci.yml": cancellingWorkflow})
			}
			if _, err := engine.Start(context.Background(), StartOptions{Name: "join-race", Repositories: []string{"acme/library"}}, nil); err != nil {
				t.Fatal(err)
			}
			repo := "acme/app"
			if existing {
				repo = "acme/library"
			}
			reads := 0
			load := func(name string) (Stream, error) {
				reads++
				if reads == 2 {
					if err := os.Remove(engine.Store.statePath(name)); err != nil {
						t.Fatal(err)
					}
				}
				return engine.Store.Load(name)
			}
			_, err := engine.joinWithLoad(context.Background(), JoinOptions{Name: "join-race", Repository: repo}, load)
			if !errors.Is(err, ErrNotFound) || reads != 2 {
				t.Fatalf("reads=%d error=%v", reads, err)
			}
			want := 2
			if existing {
				want = 1
			}
			if len(worktrees.created) != want {
				t.Fatalf("created=%d want=%d", len(worktrees.created), want)
			}
		})
	}
}
