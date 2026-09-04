package sessionparkreceive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == worktrees.SecureCleanupGitHelperArgument {
		os.Exit(worktrees.RunSecureCleanupGitHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == worktrees.SecureStageGitHelperArgument {
		os.Exit(worktrees.RunSecureStageGitHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == worktrees.SecureCanonicalGitHelperArgument {
		os.Exit(worktrees.RunSecureCanonicalGitHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == worktrees.SecureStageCanonicalGitHelperArgument {
		os.Exit(worktrees.RunSecureStageCanonicalGitHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == worktrees.SecureRenameGitHelperArgument {
		os.Exit(worktrees.RunSecureRenameGitHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestReceiveTwoMemberBundleUsesRealGitAndWorkLogBarrier(t *testing.T) {
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	home := filepath.Join(root, ".wb")
	if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, home)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	secret := "private-two-member-continuation-marker"
	request := sessionpark.RemoteRequest{
		SchemaVersion: sessionpark.RequestSchemaVersion, ResumeID: "resume-real-two", ParkedSessionID: "park-real-two",
		SuccessorWBSessionID: "wbs-real-two-successor", PredecessorWBSessionID: "wbs-real-two-source",
		SourceMachine: "source", TargetMachine: "target", SourceRuntime: "codex", SourceModel: "gpt-5",
		Continuation: secret, CreatedAt: time.Unix(100, 0).UTC(),
	}
	for index, repository := range []string{"one", "two"} {
		remote, commit := createParkRemote(t, root, repository)
		request.Members = append(request.Members, sessionpark.RemoteMember{
			MemberID: fmt.Sprintf("m-%03d-abcdef%02d", index+1, index+1), Repository: "acme/" + repository,
			RepositoryRemote: remote, Branch: "feature/park", Commit: commit,
			SourceWorkLogReference: "worklog:parked-two/run/" + strings.Repeat(string(rune('a'+index)), 64),
		})
	}
	raw, err := sessionpark.EncodeEnvelope(sessionpark.Envelope{SchemaVersion: sessionpark.EnvelopeSchemaVersion, Kind: sessionpark.EnvelopeKind, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	store := sessionpark.NewTargetStore(filepath.Join(home, sessionpark.TargetDirName))
	barrierChecked := false
	result, err := Receive(context.Background(), Options{
		Store: store, ProjectsRoot: projectsRoot, LocalMachine: "target", RawEnvelope: raw,
		InspectSuccessor: func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
			return sessionlaunch.Result{}, sessionlaunch.ErrNotReleased
		},
		StartSuccessor: func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
			record := session.Record{PID: os.Getpid(), WBSessionID: request.SuccessorWBSessionID,
				PredecessorWBSessionID: request.PredecessorWBSessionID, Machine: request.TargetMachine,
				Runtime: request.SourceRuntime, Model: request.SourceModel, TmuxName: "wb-session-" + request.SuccessorWBSessionID,
				HandoffID: request.ResumeID, StartedAt: time.Unix(200, 0).UTC()}
			reference, err := options.BeforeRelease(ctx, sessionlaunch.Prepared{Authority: *options.Authority,
				RequestDigest: sessionmove.Digest(options.Authority.AggregateDigest), Session: record,
				AttemptID: "000001-11111111111111111111111111111111", AttemptIndex: 1,
				WorktreeDir: options.WorktreeDir, PinnedCommit: options.PinnedCommit})
			if err != nil {
				return sessionlaunch.Result{}, err
			}
			for _, member := range request.Members {
				path := sessionMemberPath(t, projectsRoot, request.ResumeID+"-"+member.MemberID, member.Repository)
				assertPreparedParkedMember(t, projectsRoot, path, request, member, record)
			}
			barrierChecked = true
			return sessionlaunch.Result{HandoffID: request.ResumeID, WBSessionID: record.WBSessionID,
				PredecessorWBSessionID: record.PredecessorWBSessionID, TargetMachine: record.Machine, PID: record.PID,
				AttemptID: "000001-11111111111111111111111111111111", AttemptIndex: 1, TmuxName: record.TmuxName,
				Runtime: record.Runtime, Model: record.Model, TargetWorkLogRef: reference, WorktreeDir: options.WorktreeDir,
				PinnedCommit: options.PinnedCommit, StartedAt: record.StartedAt}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !barrierChecked || result.Receipt == nil || len(result.Receipt.Members) != 2 || result.Phase != PhaseCompleted {
		t.Fatalf("result=%#v barrier=%t", result, barrierChecked)
	}
	for index, member := range request.Members {
		receiptMember := result.Receipt.Members[index]
		if receiptMember.Repository != member.Repository || receiptMember.Commit != member.Commit || receiptMember.TargetPath == "" ||
			receiptMember.TargetWorkLogReference == "" {
			t.Fatalf("receipt member %d = %#v", index, receiptMember)
		}
		if got := gitOutput(t, receiptMember.TargetPath, "rev-parse", "HEAD"); got != member.Commit {
			t.Fatalf("member %d HEAD = %s, want %s", index, got, member.Commit)
		}
		if got := gitOutput(t, receiptMember.TargetPath, "branch", "--show-current"); got != receiptMember.Pin {
			t.Fatalf("member %d branch = %s, want %s", index, got, receiptMember.Pin)
		}
	}
	// Model the post-resume landing receipt: each accepted member commit is
	// now contained in the target branch, while the receiver still owns the
	// isolated pin branch and worktree. Cleanup must discover the physical
	// session namespaces from the logical effort and retire both safely.
	for _, member := range request.Members {
		runGit(t, member.RepositoryRemote, "update-ref", "refs/heads/main", member.Commit)
	}
	installNoPullRequestsGitHubFixture(t)
	cleaned, err := worktrees.Cleanup(context.Background(), worktrees.CleanupOptions{
		ProjectsRoot: projectsRoot, Task: "parked-two", Base: "main", Apply: true, Workers: 2,
	})
	if err != nil {
		t.Fatalf("cleanup resumed parked members: %v", err)
	}
	if len(cleaned.Results) != len(request.Members) {
		t.Fatalf("cleanup results = %#v, want %d members", cleaned.Results, len(request.Members))
	}
	for _, member := range request.Members {
		var targetPath string
		for _, cleanedMember := range cleaned.Results {
			if cleanedMember.Repository == member.Repository {
				targetPath = cleanedMember.WorktreeDir
				if !cleanedMember.Applied || !cleanedMember.WorktreeGone || !cleanedMember.BranchDeleted {
					t.Fatalf("cleanup result for %s = %#v, want terminal removal", member.Repository, cleanedMember)
				}
			}
		}
		if targetPath == "" {
			t.Fatalf("cleanup did not report member %s", member.Repository)
		}
		if _, statErr := os.Stat(targetPath); !os.IsNotExist(statErr) {
			t.Fatalf("resumed worktree %s remains after cleanup: %v", targetPath, statErr)
		}
		canonical := filepath.Join(projectsRoot, strings.Replace(member.Repository, "/", string(filepath.Separator), 1))
		if gitRefExists(canonical, "refs/heads/"+sessionpark.MemberPin(request.ResumeID, member.MemberID)) {
			t.Fatalf("pin branch for %s remains after cleanup", member.Repository)
		}
	}
	assertTreeDoesNotContain(t, filepath.Join(home, "worklogs"), secret)
	assertTreeDoesNotContain(t, filepath.Join(home, "worktrees"), secret)
}

func createParkRemote(t *testing.T, root, repository string) (string, string) {
	t.Helper()
	remote := filepath.Join(root, "remotes", "acme", repository+".git")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(root, "seed-"+repository)
	runGit(t, root, "clone", remote, seed)
	runGit(t, seed, "config", "user.name", "WB Test")
	runGit(t, seed, "config", "user.email", "wb@example.test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte(repository+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "README.md")
	runGit(t, seed, "-c", "commit.gpgSign=false", "commit", "-m", "initial")
	runGit(t, seed, "push", "origin", "main")
	runGit(t, seed, "checkout", "-b", "feature/park")
	if err := os.WriteFile(filepath.Join(seed, "park.txt"), []byte("park "+repository+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "park.txt")
	runGit(t, seed, "-c", "commit.gpgSign=false", "commit", "-m", "park")
	runGit(t, seed, "push", "origin", "feature/park")
	return remote, gitOutput(t, seed, "rev-parse", "HEAD")
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func gitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(arguments, " "), err)
	}
	return strings.TrimSpace(string(output))
}

func gitRefExists(directory, reference string) bool {
	command := exec.Command("git", "-C", directory, "show-ref", "--verify", "--quiet", reference)
	return command.Run() == nil
}

func sessionMemberPath(t *testing.T, projectsRoot, resumeID, repository string) string {
	t.Helper()
	owner, name, found := strings.Cut(repository, "/")
	if !found {
		t.Fatalf("repository = %q", repository)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Join(projectsRoot, owner, name))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(canonical, ".worktrees", "session-"+resumeID)
}

func assertPreparedParkedMember(t *testing.T, projectsRoot, path string, request sessionpark.RemoteRequest, member sessionpark.RemoteMember, record session.Record) {
	t.Helper()
	if gitOutput(t, path, "rev-parse", "HEAD") != member.Commit {
		t.Fatalf("prepared member %s HEAD changed", member.MemberID)
	}
	raw, err := os.ReadFile(filepath.Join(path, ".wb-worklog", "recovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	var projection struct {
		ClaimID   string `json:"claim_id"`
		Lifecycle string `json:"lifecycle"`
	}
	if err := json.Unmarshal(raw, &projection); err != nil || projection.ClaimID == "" || projection.Lifecycle != "active" {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
	eventsRaw, err := os.ReadFile(filepath.Join(path, ".wb", "local", "worklog", "events.jsonl"))
	if err != nil || !strings.Contains(string(eventsRaw), record.Runtime+"/"+record.WBSessionID) ||
		!strings.Contains(string(eventsRaw), request.ResumeID) || !strings.Contains(string(eventsRaw), member.MemberID) {
		t.Fatalf("member %s lacks target owner barrier: %v\n%s", member.MemberID, err, eventsRaw)
	}
	var events []struct {
		Version int `json:"version"`
	}
	if err := decodeJSONLines(eventsRaw, &events); err != nil {
		t.Fatalf("member %s has malformed local Work Log events: %v", member.MemberID, err)
	}
	for index, event := range events {
		if event.Version != 1 {
			t.Fatalf("member %s event %d version = %d, want 1", member.MemberID, index, event.Version)
		}
	}
}

func assertTreeDoesNotContain(t *testing.T, root, secret string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(raw), secret) {
			t.Errorf("private continuation leaked into %s", path)
		}
		return nil
	})
}

func decodeJSONLines(raw []byte, target any) error {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	encoded := []byte("[")
	for index, line := range lines {
		if index > 0 {
			encoded = append(encoded, ',')
		}
		encoded = append(encoded, line...)
	}
	encoded = append(encoded, ']')
	return json.Unmarshal(encoded, target)
}

func installNoPullRequestsGitHubFixture(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := "#!/bin/sh\nif [ \"$1\" = api ]; then printf '%s\\n' '[]'; exit 0; fi\necho \"unexpected gh command: $*\" >&2\nexit 2\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
