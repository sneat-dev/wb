//go:build e2e && !windows

package worktrees

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // Existing native repository and gh response fixtures pin process-wide configuration.
func TestE2EDirtyLegacyGCOrdersNativeInventoryBeforePhaseRefusals(t *testing.T) {
	fixture := newGitFixture(t)
	addRepositoryToFixture(t, fixture, "second")
	const task = "gc-native-phases"
	created, heads := prepareMergedTaskInRepositories(t, fixture, task, "app", "second")
	now := time.Now().UTC()
	installMergedPullRequestFixtures(t, heads, now)
	opts := GCOptions{ProjectsRoot: fixture.projectsRoot, Tasks: []string{task}, SessionFreshness: DisableSessionFreshness, SkipSizes: true, Now: func() time.Time { return now.Add(time.Hour) }}
	control, err := GC(t.Context(), opts)
	if err != nil || len(control.Entries) != 2 || !control.Entries[0].Eligible || !control.Entries[1].Eligible || control.Entries[0].WorktreeDir >= control.Entries[1].WorktreeDir {
		t.Fatalf("actual native inventory/seal-preflight/tie-order prerequisite: %+v %v", control.Entries, err)
	}
	private := map[string][]byte{}
	for _, item := range created {
		if _, _, _, err := activeWorkLogClaim(fixture.home, item.WorktreeDir); err != nil {
			t.Fatalf("actual private claim prerequisite: %v", err)
		}
		raw, err := os.ReadFile(item.WorkLogPath)
		if err != nil {
			t.Fatal(err)
		}
		private[item.WorkLogPath] = raw
	}
	for _, kind := range []string{"sweep", "apply"} {
		//nolint:paralleltest // The two phase cases share this immutable native fixture and its process environment.
		t.Run(kind, func(t *testing.T) {
			alias := filepath.Join(t.TempDir(), "projects-alias")
			if err := os.Symlink(fixture.projectsRoot, alias); err != nil {
				t.Fatal(err)
			}
			target, err := os.Readlink(alias)
			if err != nil {
				t.Fatal(err)
			}
			localOpts := opts
			localOpts.ProjectsRoot = alias
			localOpts.Apply = kind == "apply"
			observed := false
			var cause error
			outcome, err := gcObserved(t.Context(), localOpts, func(stage string) {
				if stage != kind {
					return
				}
				if observed {
					t.Fatal("phase repeated")
				}
				observed = true
				if err := os.Rename(alias, alias+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(alias, alias); err != nil {
					t.Fatal(err)
				}
				if kind == "sweep" {
					_, cause = wbhome.Resolve(alias)
				} else {
					_, cause = wbhome.Root(alias)
				}
				if cause == nil {
					t.Fatal("actual next native resolver did not refuse the alias loop")
				}
			})
			want := ""
			if cause != nil {
				want = cause.Error()
			}
			if kind == "sweep" {
				want = "sweep empty task shells: " + want
			}
			if !observed || cause == nil || err == nil || err.Error() != want || outcome.SchemaVersion != 1 || outcome.Apply != localOpts.Apply || len(outcome.Entries) != 2 {
				t.Fatalf("native %s partial outcome: %+v %v control=%v observed=%v", kind, outcome, err, cause, observed)
			}
			for _, entry := range outcome.Entries {
				if !entry.Eligible || entry.Applied || entry.Error != "" {
					t.Fatalf("refusal changed inventory authority: %+v", entry)
				}
			}
			if got, err := os.Readlink(alias + "-retained"); err != nil || got != target {
				t.Fatalf("original alias changed: %q %v", got, err)
			}
			if err := os.Remove(alias); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(alias+"-retained", alias); err != nil {
				t.Fatal(err)
			}
			if got, err := os.Readlink(alias); err != nil || got != target {
				t.Fatalf("alias restoration: %q %v", got, err)
			}
			for index, item := range created {
				if got := gitTestOutput(t, item.WorktreeDir, "rev-parse", "HEAD"); got != heads[index] {
					t.Fatalf("phase refusal changed HEAD: %s", got)
				}
				raw, err := os.ReadFile(item.WorkLogPath)
				if err != nil || !reflect.DeepEqual(raw, private[item.WorkLogPath]) {
					t.Fatalf("phase refusal changed private claim: %q %v", raw, err)
				}
			}
		})
	}
}

func TestE2EDirtyLegacyGCPropagatesNativeListAdmissionRefusal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, control := ListWithDiagnostics(t.Context(), ListOptions{ProjectsRoot: root, Base: "invalid branch"})
	if control == nil {
		t.Fatal("actual native branch admission control unexpectedly succeeded")
	}
	outcome, err := GC(t.Context(), GCOptions{ProjectsRoot: root, Base: "invalid branch"})
	if err == nil || err.Error() != control.Error() || !reflect.DeepEqual(outcome, GCOutcome{}) || !strings.Contains(err.Error(), "invalid base branch") {
		t.Fatalf("native List admission refusal: %+v %v control=%v", outcome, err, control)
	}
}
