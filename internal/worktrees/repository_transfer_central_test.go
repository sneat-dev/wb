package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

//nolint:paralleltest // native fixture uses isolated process-wide Git/WB configuration.
func TestRepositoryTransferPreservedCentralPathResolvesCompletedIdentity(t *testing.T) {
	for _, hosted := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy canonical", true: "literal forge canonical"}[hosted], func(t *testing.T) {
			f := newRepositoryTransferFixture(t)
			if hosted {
				canonical := filepath.Join(f.projectsRoot, "github.com", "acme", "app")
				if err := os.MkdirAll(filepath.Dir(canonical), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(f.canonical, canonical); err != nil {
					t.Fatal(err)
				}
				f.canonical = canonical
				f.destination = filepath.Join(f.projectsRoot, "github.com", "newco", "renamed")
				if err := os.MkdirAll(filepath.Dir(f.destination), 0755); err != nil {
					t.Fatal(err)
				}
			}
			mustWriteBranchConfig(t, filepath.Join(filepath.Dir(f.projectsRoot), "config", "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  store: central\n")
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: f.projectsRoot, Operation: "central-transfer", WorkLog: WorkLogOptions{Model: "unknown"}})
			if err != nil {
				t.Fatal(err)
			}
			path := created[0].WorktreeDir
			claim, _, claimPath, err := activeWorkLogClaim(f.home, path)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(claimPath)
			if err != nil {
				t.Fatal(err)
			}
			f.moveRemote(t)
			options := RepositoryRelocateOptions{ProjectsRoot: f.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: f.newRemote, DefaultBranch: "main", Apply: true}
			canonicalPath := func(root, repository string) (string, error) {
				if repository == "acme/app" {
					return f.canonical, nil
				}
				return f.destination, nil
			}
			result, err := relocateRepositoryWithReads(context.Background(), options, canonicalPath, gitRawOutput)
			if err != nil || !result.Applied || len(result.ReceiptPaths) != 1 {
				t.Fatalf("transfer=%+v %v", result, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("central path moved", err)
			}
			after, err := os.ReadFile(claimPath)
			if err != nil || string(after) != string(before) {
				t.Fatal("immutable claim rewritten", err)
			}
			view, _, err := LogShow(context.Background(), f.projectsRoot, path)
			if err != nil || view.Claim == nil || view.Claim.Repository != "newco/renamed" || view.Claim.ClaimID != claim.ClaimID || view.Claim.Worktree != path {
				t.Fatalf("resolved view=%+v %v", view.Claim, err)
			}
			guard, err := Guard(context.Background(), path, GuardOptions{ProjectsRoot: f.projectsRoot})
			if err != nil || guard.External {
				t.Fatalf("managed guard=%+v %v", guard, err)
			}
			outcome, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: f.projectsRoot, Task: "central-transfer", Filter: "newco/renamed"})
			if err != nil || len(outcome.Results) != 1 || outcome.Results[0].Repository != "newco/renamed" {
				all, allErr := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: f.projectsRoot, Task: "central-transfer"})
				t.Fatalf("inventory=%+v %v all=%+v %v", outcome, err, all, allErr)
			}
			for _, task := range []string{"central-transfer", ""} {
				outcome, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: f.projectsRoot, Task: task, Filter: "newco/renamed"})
				if err != nil || len(outcome.Results) != 1 || outcome.Results[0].External {
					t.Fatalf("new identity inventory=%+v %v", outcome, err)
				}
			}
			resumed, err := Create(context.Background(), []string{"newco/renamed"}, CreateOptions{ProjectsRoot: f.projectsRoot, Operation: "central-transfer", Resume: true, WorkLog: WorkLogOptions{Model: "unknown"}})
			if err != nil || len(resumed) != 1 || resumed[0].WorktreeDir != path || resumed[0].Action != "resumed" {
				t.Fatalf("resume=%+v %v", resumed, err)
			}
			after, err = os.ReadFile(claimPath)
			if err != nil || string(after) != string(before) {
				t.Fatal("resume rewrote original claim", err)
			}
		})
	}
}

//nolint:paralleltest // native fixture uses isolated process-wide Git/WB configuration.
func TestRepositoryTransferCentralEvidenceCannotBeReplacedByOriginOrBrokenReceipt(t *testing.T) {
	for _, kind := range []string{"missing receipt", "broken receipt", "wrong origin", "same slug other forge", "foreign push origin"} {
		t.Run(kind, func(t *testing.T) {
			f := newRepositoryTransferFixture(t)
			mustWriteBranchConfig(t, filepath.Join(filepath.Dir(f.projectsRoot), "config", "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  store: central\n")
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: f.projectsRoot, Operation: "central-negative", WorkLog: WorkLogOptions{Model: "unknown"}})
			if err != nil {
				t.Fatal(err)
			}
			path := created[0].WorktreeDir
			f.moveRemote(t)
			result, err := RelocateRepository(context.Background(), RepositoryRelocateOptions{ProjectsRoot: f.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: f.newRemote, DefaultBranch: "main", Apply: true})
			if err != nil || !result.Applied {
				t.Fatal(result, err)
			}
			switch kind {
			case "missing receipt":
				err = os.Remove(result.ReceiptPaths[0])
			case "broken receipt":
				err = os.WriteFile(result.ReceiptPaths[0], []byte("{broken"), 0600)
			case "wrong origin":
				gitTest(t, path, "remote", "set-url", "origin", f.oldRemote)
			case "same slug other forge":
				gitTest(t, path, "remote", "set-url", "origin", "https://other.example/newco/renamed.git")
			case "foreign push origin":
				gitTest(t, path, "remote", "set-url", "--push", "origin", "https://other.example/newco/renamed.git")
			}
			if err != nil {
				t.Fatal(err)
			}
			if corroboratedPreservedRepositoryTransfer(f.projectsRoot, path, "central-negative", "newco/renamed") {
				t.Fatal("unproved identity granted managed placement")
			}
			if _, err := Guard(context.Background(), path, GuardOptions{ProjectsRoot: f.projectsRoot}); err == nil {
				t.Fatal("adopted fallback bypassed missing current identity proof")
			}
			view, _, err := LogShow(context.Background(), f.projectsRoot, path)
			if err == nil && view.Claim != nil && view.Claim.Repository == "newco/renamed" {
				t.Fatal("invalid proof projected new repository")
			}
		})
	}
}

//nolint:paralleltest // adoption fixture has isolated process-wide Git/WB configuration.
func TestRepositoryTransferProofDoesNotTurnAdoptedExternalPathIntoManagedGeometry(t *testing.T) {
	f := newRepositoryTransferFixture(t)
	path := filepath.Join(filepath.Dir(f.projectsRoot), "external", "adopted-app")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, f.canonical, "worktree", "add", "-b", "adopted-external", path, "main")
	if result, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: f.projectsRoot, Base: "main", Path: path, Apply: true, Initiator: "test-operator"}); err != nil || len(result) != 1 {
		t.Fatal(result, err)
	}
	claim, _, _, err := activeWorkLogClaim(f.home, path)
	if err != nil {
		t.Fatal(err)
	}
	if corroboratedPreservedRepositoryTransfer(f.projectsRoot, path, claim.Task, "acme/app") {
		t.Fatal("external adoption became managed central geometry")
	}
	guard, err := Guard(context.Background(), path, GuardOptions{ProjectsRoot: f.projectsRoot})
	if err != nil || !guard.External {
		t.Fatal("valid external adoption lost its guard", guard, err)
	}
	if !filterMatchesClaimRepository(f.home, "acme/app", path, claim) {
		t.Fatal("original repository filter lost active claim")
	}
	if corroboratedPreservedRepositoryTransfer("bad\x00root", path, claim.Task, "acme/app") {
		t.Fatal("unresolvable project root admitted")
	}
}

//nolint:paralleltest // native fixture uses isolated process-wide Git/WB configuration.
func TestRepositoryTransferRemoteBindingSurvivesLaterLayoutRelocation(t *testing.T) {
	f := newRepositoryTransferFixture(t)
	mustWriteBranchConfig(t, filepath.Join(filepath.Dir(f.projectsRoot), "config", "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  store: central\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: f.projectsRoot, Operation: "transfer-then-layout", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	original := created[0].WorktreeDir
	f.moveRemote(t)
	if result, err := RelocateRepository(context.Background(), RepositoryRelocateOptions{ProjectsRoot: f.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: f.newRemote, DefaultBranch: "main", Apply: true}); err != nil || !result.Applied {
		t.Fatal(result, err)
	}
	moved, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: f.projectsRoot, Task: "transfer-then-layout", To: "local", Apply: true})
	if err != nil || len(moved.Results) != 1 || !moved.Results[0].Applied {
		t.Fatal("layout after transfer", moved, err)
	}
	path := moved.Results[0].Destination
	if path == original {
		t.Fatal("layout fixture did not move")
	}
	if _, _, _, err := activeWorkLogClaim(f.home, path); err != nil {
		t.Fatal("valid chain rejected", err)
	}
	gitTest(t, path, "remote", "set-url", "origin", "https://other.example/newco/renamed.git")
	if _, _, _, err := activeWorkLogClaim(f.home, path); err == nil {
		t.Fatal("layout receipt erased full repository remote binding")
	}
	if _, err := Guard(context.Background(), path, GuardOptions{ProjectsRoot: f.projectsRoot}); err == nil {
		t.Fatal("guard admitted a foreign origin after chained moves")
	}
}
