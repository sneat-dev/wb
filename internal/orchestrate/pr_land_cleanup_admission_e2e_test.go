//go:build e2e && !windows

package orchestrate

import (
	"context"
	"flag"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestE2ELandingCleanupAdmissionRefusesBeforeMergeAndPreservesCheckout(t *testing.T) {
	const marker = "WB_LANDING_CLEANUP_FIXTURE_CHILD"
	if os.Getenv(marker) == t.Name() {
		fixtureLandingCleanupAdmission(t)
		return
	}
	t.Parallel()
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.v")
	command.Env = append(os.Environ(), marker+"="+t.Name())
	if coverFlag := flag.Lookup("test.gocoverdir"); testing.CoverMode() != "" && coverFlag != nil && coverFlag.Value.String() != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+coverFlag.Value.String())
		command.Env = append(command.Env, "GOCOVERDIR="+coverFlag.Value.String())
	}
	output, err := command.CombinedOutput()
	t.Logf("isolated admission fixture:\n%s", output)
	if err != nil {
		t.Fatalf("isolated admission fixture: %v", err)
	}
}

func fixtureLandingCleanupAdmission(t *testing.T) {
	fixture := newLandFixture(t, "bump/admission", "go.mod")
	created, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{ProjectsRoot: fixture.projects, Operation: "admission", Branch: "bump/admission", BranchChosen: true, Resume: true, WorkLog: worktrees.WorkLogOptions{Model: "unknown"}})
	if err != nil || len(created) != 1 {
		t.Fatal(created, err)
	}
	checkout := created[0].WorktreeDir
	options := landOptions(fixture)
	options.Keep = true
	view := orchCovPullRequestView(t, `{"head":{"ref":"bump/admission"},"base":{"ref":"main"}}`)
	evidence := filepath.Join(checkout, "go.work")
	original := []byte("{malformed\n")
	if err = os.WriteFile(evidence, original, 0600); err != nil {
		t.Fatal(err)
	}
	wrongBranch := view
	wrongBranch.Head.Ref = "unrelated"
	if got := preflightLandingCleanup(context.Background(), options, wrongBranch, "7", true); got != nil {
		t.Fatalf("unrelated branch refused: %+v", got)
	}
	wrongRepository := options
	wrongRepository.Repository = "acme/ap"
	if got := preflightLandingCleanup(context.Background(), wrongRepository, view, "7", true); got != nil {
		t.Fatalf("substring repository match refused: %+v", got)
	}
	if got := preflightLandingCleanup(context.Background(), options, view, "7", false); got == nil || got.code != "cleanup-blocked-dirty" {
		t.Fatalf("dirty admission=%+v", got)
	}
	for _, fault := range []string{"malformed workspace", "corrupt stream", "live workspace"} {
		if fault == "corrupt stream" {
			if err = os.Remove(evidence); err != nil {
				t.Fatal(err)
			}
			store, openErr := streams.Open(fixture.projects)
			if openErr != nil {
				t.Fatal(openErr)
			}
			evidence = filepath.Join(store.Root, "broken", "stream.json")
			if err = os.MkdirAll(filepath.Dir(evidence), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(evidence, original, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if fault == "live workspace" {
			if err = os.RemoveAll(filepath.Dir(evidence)); err != nil {
				t.Fatal(err)
			}
			evidence = filepath.Join(checkout, "go.work")
			original = []byte("go 1.27\nuse /elsewhere/unpublished\n")
			if err = os.WriteFile(evidence, original, 0600); err != nil {
				t.Fatal(err)
			}
		}
		result, landErr := LandPullRequest(context.Background(), options)
		code := "cleanup-unverifiable"
		if fault == "live workspace" {
			code = "cleanup-blocked-live-link"
		}
		if landErr != nil || result.Outcome != LandRefused || result.RefusalCode != code || !strings.Contains(result.Reason, evidence) {
			t.Fatalf("%s landed: %+v %v", fault, result, landErr)
		}
		if fixture.readState(t, "merged") != "false" || fixtureHasMarker(fixture, "auto-merge") {
			t.Fatal("irreversible landing ran before admission")
		}
		if _, err = os.Stat(checkout); err != nil {
			t.Fatal("refusal removed checkout", err)
		}
		raw, readErr := os.ReadFile(evidence)
		if readErr != nil || string(raw) != string(original) {
			t.Fatal("refusal changed evidence", readErr)
		}
	}
	// A WB-home resolution failure still uses the independent workspace signal.
	projects := filepath.Join(t.TempDir(), "cycle")
	if err := os.Symlink(projects, projects); err != nil {
		t.Fatal(err)
	}
	if refusal := refuseLinkedWorktree(projects, worktrees.ListResult{WorktreeDir: checkout}); refusal == nil || refusal.code != "cleanup-blocked-live-link" {
		t.Fatalf("nil-store fallback missed workspace link: %+v", refusal)
	}
	if err := os.Remove(evidence); err != nil {
		t.Fatal(err)
	}
	if refusal := refuseLinkedWorktree(projects, worktrees.ListResult{WorktreeDir: checkout}); refusal != nil {
		t.Fatalf("nil-store clean fallback refused: %+v", refusal)
	}

}
