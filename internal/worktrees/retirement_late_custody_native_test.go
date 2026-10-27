//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktreeretire"
)

// TestE2ERetirementRefusesLateIgnoredFilesAndCustodyReadFailures drives the real
// retirement facade. Ignored material that appears after admission but before
// the deletion fence refuses and leaves the checkout, source and archive in
// place. A separate real retirement then proves the ordered custody failure:
// admission and BeforeDeletion succeed, the admitted custody evidence is
// replaced immediately before local removal, and that inspection error refuses
// without removing the checkout, local branch or preserved evidence.
//
//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
func TestE2ERetirementRefusesLateIgnoredFilesAndCustodyReadFailures(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retire-late-ignored", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "retire-late-ignored")
	if err := os.WriteFile(filepath.Join(worktree, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", ".gitignore")
	gitTest(t, worktree, "commit", "-m", "test: ignore build output")
	gitTest(t, worktree, "push", "origin", "retire-late-ignored")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	testenv.ConfigureGitAutoMaintenanceOff(t, archive)
	_, err = Retire(context.Background(), RetireOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "retire-late-ignored", ArchiveRemote: archive, Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		afterPhase: func(string) error {
			if err := os.MkdirAll(filepath.Join(worktree, "build"), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(worktree, "build", "late-secret.bin"), []byte("safe fake secret\n"), 0o600)
		},
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "ignored worktree files would be lost") {
		t.Fatalf("late ignored material retirement = %v", err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("late ignored material removed the checkout: %v", err)
	}
	if branch := gitTestOutput(t, fixture.canonical, "for-each-ref", "--format=%(objectname)", "refs/heads/retire-late-ignored"); strings.TrimSpace(branch) == "" {
		t.Fatal("late ignored material deleted the local branch")
	}

	// Drive the same real ordered operation for the custody failure: a real
	// admitted custody snapshot survives the admission and BeforeDeletion
	// fences, then its durable evidence is physically replaced immediately
	// before RemoveLocal's own inspection. The ordered observations below prove
	// the failure is that late inspection error, not a fence mismatch or the
	// earlier late-ignored-material scenario.
	fixture = newGitFixture(t)
	task := "retire-late-custody-read"
	created, err = Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: task, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree = created[0].WorktreeDir
	directory := filepath.Join(fixture.home, session.DirName)
	current := custodyRegisterSession(t, directory, "wbs-current", os.Getpid())
	SetSessionResolver(func() (AgentIdentity, bool) { return custodyIdentity(current), true })
	t.Cleanup(func() { SetSessionResolver(nil) })
	custodyTakeover(t, fixture, worktree, current.WBSessionID, custodyLegacyExpected(t, fixture, worktree))
	checkout, err := ResolveCollaborationCheckout(context.Background(), fixture.projectsRoot, worktree)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(fixture.home, "worktree-collaboration", checkout.ID+".json")
	gitTest(t, worktree, "push", "-u", "origin", task)
	originalRemoteSHA := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	archive = filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	testenv.ConfigureGitAutoMaintenanceOff(t, archive)

	type custodyObservation struct {
		fence retireCustodyFence
		err   error
	}
	var observations []custodyObservation
	var events []string
	ports := productionRetireEntryPorts()
	productionCheck := ports.checkCustody
	ports.checkCustody = func(ctx context.Context, root, home string, entry ListResult) (retireCustodyFence, error) {
		fence, checkErr := productionCheck(ctx, root, home, entry)
		observations = append(observations, custodyObservation{fence: fence, err: checkErr})
		if checkErr != nil {
			events = append(events, "custody-error")
		} else {
			events = append(events, "custody-ok")
		}
		return fence, checkErr
	}
	result, err := retireWithEntryPorts(context.Background(), RetireOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, ArchiveRemote: archive, Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
		afterPhase: func(phase string) error {
			events = append(events, "phase:"+phase)
			if phase != "original_deleted" {
				return nil
			}
			events = append(events, "mutate-custody-evidence")
			return os.WriteFile(statePath, []byte("{corrupt"), 0o600)
		},
	}, ports)
	if err == nil || !strings.Contains(err.Error(), "retirement custody authority") {
		t.Fatalf("late custody evidence read failure = %v", err)
	}
	if strings.Contains(err.Error(), "custody changed before local removal") {
		t.Fatalf("late custody read failure was masked as a fence mismatch: %v", err)
	}
	// Observations 0 and 1 are the admission and under-lock fences, 2 is the
	// real BeforeDeletion fence, and 3 is RemoveLocal's own inspection.
	if len(observations) != 4 {
		t.Fatalf("custody observations = %d, want admission, locked, BeforeDeletion, RemoveLocal", len(observations))
	}
	for index := 0; index < 3; index++ {
		if observations[index].err != nil || !observations[index].fence.same(observations[0].fence) {
			t.Fatalf("custody observation %d = fence %+v, err %v", index, observations[index].fence, observations[index].err)
		}
	}
	if observations[3].err == nil {
		t.Fatal("local-removal custody inspection succeeded after the admitted evidence was replaced")
	}
	eventIndex := func(want string) int {
		for index, event := range events {
			if event == want {
				return index
			}
		}
		return -1
	}
	beforeDeletion := -1
	for index, event := range events {
		if event == "custody-ok" {
			beforeDeletion = index
		}
	}
	ordered := []struct {
		name  string
		index int
	}{
		{"BeforeDeletion custody", beforeDeletion},
		{"deletion intent phase", eventIndex("phase:original_delete_intent")},
		{"atomic deletion phase", eventIndex("phase:original_delete_pushed")},
		{"original_deleted phase", eventIndex("phase:original_deleted")},
		{"late custody mutation", eventIndex("mutate-custody-evidence")},
		{"RemoveLocal custody failure", eventIndex("custody-error")},
	}
	for index, step := range ordered {
		if step.index < 0 {
			t.Fatalf("ordered observation %q missing from %v", step.name, events)
		}
		if index > 0 && ordered[index-1].index >= step.index {
			t.Fatalf("ordered observations out of order: %+v", ordered)
		}
	}
	// The admitted custody evidence itself was physically replaced, and nothing
	// at or after local removal ran.
	if body, readErr := os.ReadFile(statePath); readErr != nil || string(body) != "{corrupt" {
		t.Fatalf("replaced custody evidence = %q, %v", body, readErr)
	}
	if result.Phase != "original_deleted" {
		t.Fatalf("reached phase = %q, want original_deleted", result.Phase)
	}
	if _, statErr := os.Stat(worktree); statErr != nil {
		t.Fatalf("late custody read failure removed the checkout: %v", statErr)
	}
	if head := gitTestOutput(t, worktree, "rev-parse", "HEAD"); head != result.SourceSHA {
		t.Fatalf("preserved checkout HEAD = %s, want %s", head, result.SourceSHA)
	}
	if local := gitTestOutput(t, fixture.canonical, "for-each-ref", "--format=%(objectname)", "refs/heads/"+result.Branch); local != result.SourceSHA {
		t.Fatalf("preserved local branch = %q, want %s", local, result.SourceSHA)
	}
	if original := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/"+result.Branch); original != "" {
		t.Fatalf("original remote branch survived its authorized deletion: %q", original)
	}
	retired := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/"+result.RetiredRef)
	if !strings.HasPrefix(retired, result.SourceSHA+"\t") {
		t.Fatalf("preserved retired source ref = %q, want %s", retired, result.SourceSHA)
	}
	proof := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", worktreeretire.DeletionProofRef(result))
	if !strings.HasPrefix(proof, result.SourceSHA+"\t") {
		t.Fatalf("preserved deletion proof ref = %q, want %s", proof, result.SourceSHA)
	}
	archived := gitTestOutput(t, fixture.canonical, "ls-remote", archive, "refs/heads/"+result.ArchiveRef)
	if !strings.HasPrefix(archived, result.ArchiveSHA+"\t") {
		t.Fatalf("preserved private archive ref = %q, want %s", archived, result.ArchiveSHA)
	}
	gitTest(t, fixture.canonical, "fetch", archive, "refs/heads/"+result.ArchiveRef)
	if manifest := gitTestOutput(t, fixture.canonical, "show", "FETCH_HEAD:retirement.json"); !strings.Contains(manifest, result.SourceSHA) || !strings.Contains(manifest, result.ClaimID) {
		t.Fatalf("preserved private archive manifest = %q", manifest)
	}
	if claim := gitTestOutput(t, fixture.canonical, "show", "FETCH_HEAD:worklog/run/claims/"+result.ClaimID+".json"); !strings.Contains(claim, result.ClaimID) {
		t.Fatalf("preserved private archive claim = %q", claim)
	}
	receipt, err := readRetireReport(result.ReportPath)
	if err != nil {
		t.Fatalf("durable receipt = %v", err)
	}
	if receipt.Phase != "original_deleted" || receipt.SourceSHA != result.SourceSHA ||
		receipt.OriginalRemoteSHA != originalRemoteSHA || receipt.DeleteIntentSHA != originalRemoteSHA ||
		receipt.ArchiveSHA != result.ArchiveSHA || receipt.RetiredRef != result.RetiredRef {
		t.Fatalf("durable receipt = %+v", receipt)
	}
}

// TestE2ERetiredCheckoutProofRefusesChangedCanonicalAndUnpublishedHead exercises
// the held proof against a real preserved retirement: a canonical namespace
// changed after the proof is held refuses every later Git read, and a published
// pull-request head that is a valid distinct object but not a commit (or is
// missing) refuses before source binding succeeds.
//
//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
func TestE2ERetiredCheckoutProofRefusesChangedCanonicalAndUnpublishedHead(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retired-proof-head", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "retired-proof-head")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	testenv.ConfigureGitAutoMaintenanceOff(t, archive)
	result, err := Retire(context.Background(), RetireOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "retired-proof-head", ArchiveRemote: archive, Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	})
	if err != nil || result.Phase != "complete" {
		t.Fatalf("retirement = %+v, %v", result, err)
	}
	options := RetiredCheckoutProofOptions{
		ProjectsRoot: fixture.projectsRoot, Task: result.Task, Repository: result.Repository,
		CandidateWorktree: result.Worktree, CandidateBranch: result.Branch,
		CandidateSHA: result.SourceSHA, PublishedCandidateSHA: result.SourceSHA, ArchiveRemote: archive,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
	}
	treeSHA := strings.TrimSpace(gitTestOutput(t, result.Canonical, "rev-parse", result.SourceSHA+"^{tree}"))
	if treeSHA == "" || treeSHA == result.SourceSHA {
		t.Fatalf("tree object identity = %q", treeSHA)
	}
	options.PublishedCandidateSHA = treeSHA
	if _, err := ProveRetiredCheckoutHeld(context.Background(), options); err == nil || !strings.Contains(err.Error(), "published pull-request head object") {
		t.Fatalf("noncommit published head = %v", err)
	}
	options.PublishedCandidateSHA = strings.Repeat("d", 40)
	if _, err := ProveRetiredCheckoutHeld(context.Background(), options); err == nil || !strings.Contains(err.Error(), "published pull-request head object") {
		t.Fatalf("missing published head = %v", err)
	}

	options.PublishedCandidateSHA = result.SourceSHA
	proof, err := ProveRetiredCheckoutHeld(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proof.Close() }()
	if err := os.Rename(result.Canonical, result.Canonical+"-held"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(result.Canonical, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := proof.Git(context.Background(), "rev-parse", "HEAD"); err == nil || !strings.Contains(err.Error(), "canonical repository") {
		t.Fatalf("held proof after canonical change = %v", err)
	}
}
