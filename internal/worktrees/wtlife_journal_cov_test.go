package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/testenv"
)

func marshalManifestForLifeCov(manifest Manifest) (string, error) {
	encoded, err := yaml.Marshal(manifest)
	return string(encoded), err
}

// wtLifeCovJournalWorktree creates a real Git worktree with one commit, which
// is the minimum every journal path needs to resolve its repository and branch.
func wtLifeCovJournalWorktree(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	wtLifeCovGit(t, path, "init", "--quiet", "--initial-branch=main")
	wtLifeCovGit(t, path, "config", "user.name", "WB LifeCov")
	wtLifeCovGit(t, path, "config", "user.email", "wtlifecov@example.test")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtLifeCovGit(t, path, "add", "README.md")
	wtLifeCovGit(t, path, "commit", "--quiet", "-m", "initial")
	return path
}

func wtLifeCovWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func wtLifeCovValidManifest(effort string) Manifest {
	return Manifest{
		Version:    1,
		EffortID:   effort,
		EffortKind: EffortKindFor(effort),
		Repository: "acme/app",
		Branch:     "feature/" + effort,
		Provenance: ProvenanceCreated,
	}
}

func TestWtLifeCovRepositoryRootForResolvesOwningCheckout(t *testing.T) {
	if _, err := RepositoryRootFor(context.Background(), t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "resolve worktree root") {
		t.Fatalf("non-repository root error = %v", err)
	}
	worktree := wtLifeCovJournalWorktree(t)
	subdirectory := filepath.Join(worktree, "nested")
	if err := os.Mkdir(subdirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := RepositoryRootFor(context.Background(), subdirectory)
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	if root != filepath.Clean(worktree) {
		t.Fatalf("repository root = %q, want %q", root, filepath.Clean(worktree))
	}
}

func TestWtLifeCovCheckAdmissionClassifiesEveryMissingRecord(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)

	absent := CheckAdmission(worktree, AdmissionWarn)
	if !absent.Admitted || !strings.Contains(absent.Reason, "no WB manifest") {
		t.Fatalf("absent manifest admission = %+v", absent)
	}

	wtLifeCovWriteFile(t, filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, manifestName), "not a manifest\n")
	unreadable := CheckAdmission(worktree, AdmissionWarn)
	if !unreadable.Admitted || !strings.Contains(unreadable.Reason, "manifest cannot be read") {
		t.Fatalf("unreadable manifest admission = %+v", unreadable)
	}

	if err := os.Remove(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, manifestName)); err != nil {
		t.Fatal(err)
	}
	encoded, err := marshalManifestForLifeCov(wtLifeCovValidManifest("feature.one"))
	if err != nil {
		t.Fatal(err)
	}
	wtLifeCovWriteFile(t, filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, manifestName), encoded)

	if err := os.MkdirAll(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory)); err != nil {
		t.Fatal(err)
	}
	wtLifeCovWriteFile(t, filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory), "not a directory\n")
	unreadablePrompts := CheckAdmission(worktree, AdmissionWarn)
	if !unreadablePrompts.Admitted || !strings.Contains(unreadablePrompts.Reason, "prompt sequence cannot be read") {
		t.Fatalf("unreadable prompts admission = %+v", unreadablePrompts)
	}

	if err := os.Remove(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory)); err != nil {
		t.Fatal(err)
	}
	empty := CheckAdmission(worktree, AdmissionEnforce)
	if empty.Admitted || !strings.Contains(empty.Reason, "has no recorded instruction") {
		t.Fatalf("empty prompt sequence admission = %+v", empty)
	}
	if empty.Remedy == "" {
		t.Fatalf("enforced admission has no remedy: %+v", empty)
	}
}

func TestWtLifeCovValidateManifestRejectsEveryDocumentedCorruption(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Manifest)
		wantErr string
	}{
		{name: "version", mutate: func(m *Manifest) { m.Version = 2 }, wantErr: "unsupported worktree manifest version"},
		{name: "effort path", mutate: func(m *Manifest) { m.EffortID = ".." }, wantErr: "invalid manifest effort id"},
		{name: "contradictory parent", mutate: func(m *Manifest) { m.ParentEffort = "other" }, wantErr: "contradicts effort path"},
		{name: "effort kind", mutate: func(m *Manifest) { m.EffortKind = "nope" }, wantErr: "invalid manifest effort kind"},
		{name: "created with inferred fields", mutate: func(m *Manifest) { m.InferredFields = []string{"effort_id"} }, wantErr: "created manifest cannot record inferred fields"},
		{name: "reconstructed without inferred fields", mutate: func(m *Manifest) { m.Provenance = ProvenanceReconstructed }, wantErr: "must record which fields were inferred"},
		{name: "provenance", mutate: func(m *Manifest) { m.Provenance = "guessed" }, wantErr: "invalid manifest provenance"},
		{name: "missing repository", mutate: func(m *Manifest) { m.Repository = " " }, wantErr: "must record repository and branch"},
		{name: "missing branch", mutate: func(m *Manifest) { m.Branch = "" }, wantErr: "must record repository and branch"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			manifest := wtLifeCovValidManifest("feature.one")
			testCase.mutate(&manifest)
			err := validateManifest(manifest)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("validateManifest error = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

func TestWtLifeCovWriteManifestRefusesUnsafeDestinations(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	if err := WriteManifest(worktree, Manifest{}); err == nil {
		t.Fatal("WriteManifest accepted an invalid manifest")
	}
	if err := WriteManifest(filepath.Join(t.TempDir(), "absent"), wtLifeCovValidManifest("feature.one")); err == nil {
		t.Fatal("WriteManifest accepted a missing worktree")
	}

	if err := os.MkdirAll(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, manifestName, "blocker"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := WriteManifest(worktree, wtLifeCovValidManifest("feature.one"))
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("manifest-as-directory error = %v", err)
	}
}

func TestWtLifeCovReadManifestClassifiesCorruption(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	if _, err := ReadManifest(worktree); err != errManifestNotFound {
		t.Fatalf("absent manifest error = %v, want errManifestNotFound", err)
	}

	if err := os.MkdirAll(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, manifestName, "blocker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(worktree); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("manifest-as-directory read error = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, manifestName)); err != nil {
		t.Fatal(err)
	}

	wtLifeCovWriteFile(t, filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, manifestName), "version: 9\n")
	if _, err := ReadManifest(worktree); err == nil || !strings.Contains(err.Error(), "unsupported worktree manifest version") {
		t.Fatalf("invalid manifest read error = %v", err)
	}

	encoded, err := marshalManifestForLifeCov(wtLifeCovValidManifest("feature.one"))
	if err != nil {
		t.Fatal(err)
	}
	wtLifeCovWriteFile(t, filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, manifestName), encoded)
	manifest, err := ReadManifest(worktree)
	if err != nil || manifest.EffortID != "feature.one" {
		t.Fatalf("valid manifest read = %+v, %v", manifest, err)
	}
}

func TestWtLifeCovReadAndWriteManifestRejectUnsafeJournalComponents(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	wtLifeCovWriteFile(t, filepath.Join(worktree, journalRootDirectory), "not a directory\n")

	if _, err := ReadManifest(worktree); err == nil || !strings.Contains(err.Error(), "open work-log journal component .wb") {
		t.Fatalf("file .wb read error = %v", err)
	}
	if err := WriteManifest(worktree, wtLifeCovValidManifest("feature.one")); err == nil {
		t.Fatal("WriteManifest accepted a file at .wb")
	}
	if _, err := openJournalSubdirectory(worktree, worklogDirectory, true); err == nil {
		t.Fatal("openJournalSubdirectory accepted a file at .wb")
	}
}

func TestWtLifeCovEnsureManifestIsIdempotentOnly(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	if err := EnsureManifest(worktree, wtLifeCovValidManifest("feature.one")); err != nil {
		t.Fatalf("first EnsureManifest: %v", err)
	}
	if err := EnsureManifest(worktree, wtLifeCovValidManifest("feature.two")); err != nil {
		t.Fatalf("second EnsureManifest: %v", err)
	}
	manifest, err := ReadManifest(worktree)
	if err != nil || manifest.EffortID != "feature.one" {
		t.Fatalf("EnsureManifest replaced the immutable record: %+v, %v", manifest, err)
	}
	if err := EnsureManifest(worktree, Manifest{}); err == nil {
		t.Fatal("EnsureManifest accepted an invalid manifest")
	}
}

func TestWtLifeCovEnsurePromptOnlyRecordsTheFirstInstruction(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	header := PromptHeader{Source: PromptSourceAgent}
	if err := EnsurePrompt(worktree, header, nil); err == nil {
		t.Fatal("EnsurePrompt accepted an empty prompt body")
	}
	if err := EnsurePrompt(worktree, header, []byte("first instruction\n")); err != nil {
		t.Fatalf("first EnsurePrompt: %v", err)
	}
	if err := EnsurePrompt(worktree, header, []byte("second instruction\n")); err != nil {
		t.Fatalf("second EnsurePrompt: %v", err)
	}
	prompts, err := ListPrompts(worktree)
	if err != nil || len(prompts) != 1 {
		t.Fatalf("EnsurePrompt recorded %d prompts, %v", len(prompts), err)
	}

	broken := wtLifeCovJournalWorktree(t)
	wtLifeCovWriteFile(t, filepath.Join(broken, journalRootDirectory, journalLocalDirectory, promptsDirectory), "not a directory\n")
	if err := EnsurePrompt(broken, header, []byte("instruction\n")); err == nil {
		t.Fatal("EnsurePrompt accepted an unreadable prompt sequence")
	}
}

func TestWtLifeCovWriteCreationJournalRecordsEveryInput(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	result := CreateResult{Repository: "acme/app", WorktreeDir: "", Branch: "feature/one", Base: "main", BaseSHA: "abc"}

	if err := writeCreationJournal("..", "", "", result, WorkLogOptions{}, now); err != nil {
		t.Fatalf("invalid effort path should be ignored, got %v", err)
	}

	missing := result
	missing.WorktreeDir = filepath.Join(t.TempDir(), "absent")
	if err := writeCreationJournal("feature.one", "", "", missing, WorkLogOptions{}, now); err == nil {
		t.Fatal("writeCreationJournal accepted a missing worktree")
	}

	worktree := wtLifeCovJournalWorktree(t)
	result.WorktreeDir = worktree
	if err := writeCreationJournal("feature.one", "run-1", "claim-1", result, WorkLogOptions{Initiator: "alex", originalPromptContents: []byte("do the thing\n")}, now); err != nil {
		t.Fatalf("writeCreationJournal: %v", err)
	}
	manifest, err := ReadManifest(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RunID != "run-1" || manifest.ClaimID != "claim-1" || manifest.Initiator != "alex" {
		t.Fatalf("creation manifest = %+v", manifest)
	}
	prompts, err := ListPrompts(worktree)
	if err != nil || len(prompts) != 1 {
		t.Fatalf("creation prompt count = %d, %v", len(prompts), err)
	}
	if prompts[0].Source != PromptSourceHuman {
		t.Fatalf("declared initiator prompt source = %q, want human", prompts[0].Source)
	}
	if err := writeCreationJournal("feature.one", "run-1", "claim-1", result, WorkLogOptions{originalPromptContents: []byte("do the thing\n")}, now); err != nil {
		t.Fatalf("resumed writeCreationJournal: %v", err)
	}
	if after, err := ListPrompts(worktree); err != nil || len(after) != 1 {
		t.Fatalf("resumed creation recorded %d prompts, %v", len(after), err)
	}
}

func TestWtLifeCovWriteCreationJournalReportsOwnerAndPromptFailures(t *testing.T) {
	now := time.Now().UTC()
	worktree := wtLifeCovJournalWorktree(t)
	blocked := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory)
	wtLifeCovWriteFile(t, blocked, "not a directory\n")
	result := CreateResult{Repository: "acme/app", WorktreeDir: worktree, Branch: "feature/one"}
	if err := writeCreationJournal("feature.one", "", "", result, WorkLogOptions{}, now); err == nil {
		t.Fatal("writeCreationJournal accepted an unopenable work log")
	}

	promptBroken := wtLifeCovJournalWorktree(t)
	wtLifeCovWriteFile(t, filepath.Join(promptBroken, journalRootDirectory, journalLocalDirectory, promptsDirectory), "not a directory\n")
	result.WorktreeDir = promptBroken
	err := writeCreationJournal("feature.one", "", "", result, WorkLogOptions{originalPromptContents: []byte("instruction\n")}, now)
	if err == nil || !strings.Contains(err.Error(), "open work-log journal component prompts") {
		t.Fatalf("unreadable prompt sequence error = %v", err)
	}

	locked := wtLifeCovJournalWorktree(t)
	lockPath := filepath.Join(locked, journalRootDirectory, journalLocalDirectory, promptsDirectory, ".prompts.lock", "blocker")
	if err := os.MkdirAll(lockPath, 0o755); err != nil {
		t.Fatal(err)
	}
	result.WorktreeDir = locked
	err = writeCreationJournal("feature.one", "", "", result, WorkLogOptions{originalPromptContents: []byte("instruction\n")}, now)
	if err == nil || !strings.Contains(err.Error(), "journal sequence lock") {
		t.Fatalf("locked prompt sequence error = %v", err)
	}
}

func TestWtLifeCovReconstructManifestReportsUnusableCheckouts(t *testing.T) {
	if _, err := ReconstructManifest(context.Background(), t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "reconstruct manifest") {
		t.Fatalf("non-repository reconstruction error = %v", err)
	}

	worktree := wtLifeCovJournalWorktree(t)
	if _, err := ReconstructManifest(context.Background(), worktree); err != nil {
		t.Fatalf("reconstruction: %v", err)
	}
	manifest, err := ReadManifest(worktree)
	if err != nil || manifest.Provenance != ProvenanceReconstructed {
		t.Fatalf("reconstructed manifest = %+v, %v", manifest, err)
	}
	if _, err := ReconstructManifest(context.Background(), worktree); err != nil {
		t.Fatalf("idempotent reconstruction: %v", err)
	}
}

func TestWtLifeCovReconstructManifestRejectsUnidentifiableRepository(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "owner", "-unusable")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	wtLifeCovGit(t, worktree, "init", "--quiet", "--initial-branch=main")
	_, err := computeReconstructedManifest(context.Background(), worktree)
	if err == nil || !strings.Contains(err.Error(), "cannot identify the repository") {
		t.Fatalf("unidentifiable repository error = %v", err)
	}
}

func TestWtLifeCovReconstructManifestRejectsDetachedHead(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	head := strings.TrimSpace(wtLifeCovGit(t, worktree, "rev-parse", "HEAD"))
	wtLifeCovGit(t, worktree, "update-ref", "--no-deref", "HEAD", head)
	if _, err := computeReconstructedManifest(context.Background(), worktree); err == nil ||
		!strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("detached HEAD error = %v", err)
	}
}

func TestWtLifeCovReconstructManifestRejectsUnderivableEffort(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, ".hidden", "nested", "worktree")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	wtLifeCovGit(t, worktree, "init", "--quiet", "--initial-branch=main")
	wtLifeCovGit(t, worktree, "config", "user.name", "WB LifeCov")
	wtLifeCovGit(t, worktree, "config", "user.email", "wtlifecov@example.test")
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtLifeCovGit(t, worktree, "add", "README.md")
	wtLifeCovGit(t, worktree, "commit", "--quiet", "-m", "initial")
	longBranch := strings.Repeat("b", 250)
	wtLifeCovGit(t, worktree, "branch", longBranch)
	wtLifeCovGit(t, worktree, "symbolic-ref", "HEAD", "refs/heads/"+longBranch)
	if _, err := computeReconstructedManifest(context.Background(), worktree); err == nil ||
		!strings.Contains(err.Error(), "cannot derive a valid effort path") {
		t.Fatalf("underivable effort error = %v", err)
	}
}

func TestWtLifeCovPreviewReconstructedManifestNeverWrites(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	previewed, err := PreviewReconstructedManifest(context.Background(), worktree)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if previewed.Provenance != ProvenanceReconstructed {
		t.Fatalf("preview provenance = %q", previewed.Provenance)
	}
	if _, err := ReadManifest(worktree); err != errManifestNotFound {
		t.Fatalf("preview wrote a manifest: %v", err)
	}

	if _, err := PreviewReconstructedManifest(context.Background(), t.TempDir()); err == nil {
		t.Fatal("preview accepted a non-repository")
	}

	if _, err := ReconstructManifest(context.Background(), worktree); err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	existing, err := PreviewReconstructedManifest(context.Background(), worktree)
	if err != nil || existing.Provenance != ProvenanceReconstructed {
		t.Fatalf("preview of recorded manifest = %+v, %v", existing, err)
	}
}

func TestWtLifeCovPreviewReportsPersistedManifestUnchanged(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	manifest := wtLifeCovValidManifest("feature.one")
	if err := WriteManifest(worktree, manifest); err != nil {
		t.Fatal(err)
	}
	previewed, err := PreviewReconstructedManifest(context.Background(), worktree)
	if err != nil || previewed.EffortID != "feature.one" || previewed.Provenance != ProvenanceCreated {
		t.Fatalf("preview of persisted manifest = %+v, %v", previewed, err)
	}
}

func TestWtLifeCovEffortAndRepositoryFromWorktreePath(t *testing.T) {
	if got := effortFromWorktreePath("/a/.worktrees/feature.one"); got != "feature.one" {
		t.Fatalf("default placement effort = %q", got)
	}
	if got := effortFromWorktreePath("/a/.worktrees/.hidden"); got != "" {
		t.Fatalf("invalid default placement effort = %q", got)
	}
	if got := effortFromWorktreePath("/root/feature.one/acme/app"); got != "feature.one" {
		t.Fatalf("shared placement effort = %q", got)
	}
	if got := effortFromWorktreePath("/root/.hidden/acme/app"); got != "" {
		t.Fatalf("invalid shared placement effort = %q", got)
	}

	if got := repositoryFromWorktreePath("/root/feature.one/acme/app"); got != "acme/app" {
		t.Fatalf("owned repository = %q", got)
	}
	if got := repositoryFromWorktreePath("/app"); got != "unknown/app" {
		t.Fatalf("ownerless repository = %q", got)
	}
	if got := repositoryFromWorktreePath("/app/-unusable"); got != "" {
		t.Fatalf("unusable repository = %q", got)
	}
}

func TestWtLifeCovReconstructCreationTimeFallsBackToOldestCommit(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	if err := os.RemoveAll(filepath.Join(worktree, ".git", "logs")); err != nil {
		t.Fatal(err)
	}
	got := reconstructCreationTime(context.Background(), worktree, "main")
	if got.IsZero() {
		t.Fatal("reflog-less reconstruction returned the zero time")
	}
	expected := strings.TrimSpace(wtLifeCovGit(t, worktree, "log", "--reverse", "--format=%cI", "-1", "main"))
	parsed, err := time.Parse(time.RFC3339, expected)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(parsed.UTC()) {
		t.Fatalf("reconstructed creation time = %v, want %v", got, parsed.UTC())
	}

	absent := reconstructCreationTime(context.Background(), t.TempDir(), "main")
	if !absent.IsZero() {
		t.Fatalf("non-repository creation time = %v, want zero", absent)
	}
}

func TestWtLifeCovReconstructBaseFallsBackAndGivesUp(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	if _, _, ok := reconstructBase(context.Background(), worktree, "main"); ok {
		t.Fatal("reconstructBase found a base without a remote target")
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	wtLifeCovGit(t, t.TempDir(), "init", "--bare", "--quiet", "--initial-branch=main", remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	wtLifeCovGit(t, worktree, "remote", "add", "origin", remote)
	wtLifeCovGit(t, worktree, "push", "--quiet", "-u", "origin", "main")
	base, sha, ok := reconstructBase(context.Background(), worktree, "main")
	if !ok || base != "main" || len(sha) != 40 {
		t.Fatalf("reconstructBase = (%q, %q, %t)", base, sha, ok)
	}
}

func TestWtLifeCovAppendPromptRejectsMalformedRequests(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	if _, err := AppendPrompt(worktree, PromptHeader{Source: PromptSourceAgent}, nil); err == nil ||
		!strings.Contains(err.Error(), "cannot be empty") {
		t.Fatalf("empty prompt error = %v", err)
	}
	if _, err := AppendPrompt(worktree, PromptHeader{Source: "invented"}, []byte("x\n")); err == nil ||
		!strings.Contains(err.Error(), "prompt source must be one of") {
		t.Fatalf("invalid prompt source error = %v", err)
	}
	if _, err := AppendPrompt(t.TempDir(), PromptHeader{Source: PromptSourceAgent}, []byte("x\n")); err == nil ||
		!strings.Contains(err.Error(), "resolve per-worktree exclude") {
		t.Fatalf("non-repository prompt error = %v", err)
	}

	broken := wtLifeCovJournalWorktree(t)
	wtLifeCovWriteFile(t, filepath.Join(broken, journalRootDirectory, journalLocalDirectory, promptsDirectory), "not a directory\n")
	if _, err := AppendPrompt(broken, PromptHeader{Source: PromptSourceAgent}, []byte("x\n")); err == nil {
		t.Fatal("AppendPrompt accepted an unopenable prompt directory")
	}
}

func TestWtLifeCovAppendPromptLocksAndOrdersTheSequence(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	first, err := AppendPrompt(worktree, PromptHeader{Source: PromptSourceHarness, Runtime: "codex"}, []byte("first line\nsecond line\n"))
	if err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	if first != "0000-first-line.md" {
		t.Fatalf("first prompt name = %q", first)
	}
	second, err := AppendPrompt(worktree, PromptHeader{Source: PromptSourceAgent}, []byte("!!!"))
	if err != nil {
		t.Fatalf("second prompt: %v", err)
	}
	if second != "0001-prompt.md" {
		t.Fatalf("second prompt name = %q", second)
	}

	locked := wtLifeCovJournalWorktree(t)
	if err := os.MkdirAll(filepath.Join(locked, journalRootDirectory, journalLocalDirectory, promptsDirectory, ".prompts.lock", "blocker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendPrompt(locked, PromptHeader{Source: PromptSourceAgent}, []byte("x\n")); err == nil ||
		!strings.Contains(err.Error(), "journal sequence lock") {
		t.Fatalf("locked sequence error = %v", err)
	}

	hardlinked := wtLifeCovJournalWorktree(t)
	prompts := filepath.Join(hardlinked, journalRootDirectory, journalLocalDirectory, promptsDirectory)
	if err := os.MkdirAll(prompts, 0o700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(prompts, ".prompts.lock")
	wtLifeCovWriteFile(t, original, "")
	if err := os.Link(original, original+".second"); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendPrompt(hardlinked, PromptHeader{Source: PromptSourceAgent}, []byte("x\n")); err == nil ||
		!strings.Contains(err.Error(), "not one regular file") {
		t.Fatalf("hard-linked lock error = %v", err)
	}
}

func TestWtLifeCovAppendPromptReportsUnreadableSequence(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	prompts := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory)
	if err := os.MkdirAll(prompts, 0o700); err != nil {
		t.Fatal(err)
	}
	wtLifeCovWriteFile(t, filepath.Join(prompts, "0000-broken.md"), "no frontmatter here\n")
	if _, err := AppendPrompt(worktree, PromptHeader{Source: PromptSourceAgent}, []byte("x\n")); err == nil ||
		!strings.Contains(err.Error(), "missing YAML frontmatter") {
		t.Fatalf("broken sequence error = %v", err)
	}
}

func TestWtLifeCovListPromptsClassifiesSequenceCorruption(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	missing, err := ListPrompts(worktree)
	if err != nil || missing != nil {
		t.Fatalf("absent prompts = %v, %v", missing, err)
	}

	fileJournal := wtLifeCovJournalWorktree(t)
	wtLifeCovWriteFile(t, filepath.Join(fileJournal, journalRootDirectory), "not a directory\n")
	if _, err := ListPrompts(fileJournal); err == nil || !strings.Contains(err.Error(), "open work-log journal component .wb") {
		t.Fatalf("file .wb prompt read error = %v", err)
	}

	prompts := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory)
	if err := os.MkdirAll(prompts, 0o700); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(prompts, "0000-unreadable.md")
	if err := os.MkdirAll(filepath.Join(unreadable, "blocker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ListPrompts(worktree); err == nil {
		t.Fatal("ListPrompts read an unusable prompt entry")
	}
	if err := os.RemoveAll(unreadable); err != nil {
		t.Fatal(err)
	}

	wtLifeCovWriteFile(t, filepath.Join(prompts, "0000-broken.md"), "no frontmatter here\n")
	if _, err := ListPrompts(worktree); err == nil || !strings.Contains(err.Error(), "missing YAML frontmatter") {
		t.Fatalf("broken prompt error = %v", err)
	}
	if err := os.Remove(filepath.Join(prompts, "0000-broken.md")); err != nil {
		t.Fatal(err)
	}

	wtLifeCovWriteFile(t, filepath.Join(prompts, "0001-mismatch.md"), "---\nseq: 4\nsource: agent_declared\n---\n\nbody\n")
	if _, err := ListPrompts(worktree); err == nil || !strings.Contains(err.Error(), "records seq 4 but its ordinal is 1") {
		t.Fatalf("ordinal mismatch error = %v", err)
	}
	if err := os.Remove(filepath.Join(prompts, "0001-mismatch.md")); err != nil {
		t.Fatal(err)
	}

	wtLifeCovWriteFile(t, filepath.Join(prompts, "0000-first.md"), "---\nseq: 0\nsource: agent_declared\n---\n\nbody\n")
	wtLifeCovWriteFile(t, filepath.Join(prompts, "0002-third.md"), "---\nseq: 2\nsource: agent_declared\n---\n\nbody\n")
	if _, err := ListPrompts(worktree); err == nil || !strings.Contains(err.Error(), "prompt sequence is not contiguous") {
		t.Fatalf("non-contiguous sequence error = %v", err)
	}

	headers, err := listPromptsIn(wtLifeCovClosedDirectory(t, prompts))
	if err == nil || !strings.Contains(err.Error(), "read prompt sequence") {
		t.Fatalf("closed directory list error = %v, headers %v", err, headers)
	}
}

func wtLifeCovClosedDirectory(t *testing.T, path string) *os.File {
	t.Helper()
	directory, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestWtLifeCovParsePromptHeaderRejectsMalformedFrontmatter(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "missing frontmatter", content: "just a body\n", wantErr: "missing YAML frontmatter"},
		{name: "unterminated frontmatter", content: "---\nseq: 0\nsource: agent_declared\n", wantErr: "unterminated YAML frontmatter"},
		{name: "invalid yaml", content: "---\nseq: [unclosed\n---\n\nbody\n", wantErr: "parse frontmatter"},
		{name: "invalid source", content: "---\nseq: 0\nsource: guessed\n---\n\nbody\n", wantErr: "invalid prompt source"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := parsePromptHeader([]byte(testCase.content)); err == nil ||
				!strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("parsePromptHeader error = %v, want %q", err, testCase.wantErr)
			}
		})
	}
	if header, err := parsePromptHeader([]byte("---\nseq: 3\nsource: harness_observed\n---\n\nbody\n")); err != nil || header.Seq != 3 {
		t.Fatalf("valid header = %+v, %v", header, err)
	}
}

func TestWtLifeCovPromptSlugDerivesSafeHint(t *testing.T) {
	if got := promptSlug("", []byte("First line\nsecond line\n")); got != "first-line" {
		t.Fatalf("first-line slug = %q", got)
	}
	if got := promptSlug("", []byte("!!!")); got != "prompt" {
		t.Fatalf("empty slug = %q", got)
	}
	long := strings.Repeat("a", 80)
	if got := promptSlug("", []byte(long)); len(got) != 40 {
		t.Fatalf("long slug = %q (%d runes)", got, len(got))
	}
}

func TestWtLifeCovValidEffortPathAndAncestry(t *testing.T) {
	for _, valid := range []string{"feature", "feature.one", "a.b.c-d"} {
		if !ValidEffortPath(valid) {
			t.Fatalf("ValidEffortPath(%q) = false", valid)
		}
	}
	for _, invalid := range []string{"", ".leading", "trailing.", "double..dot", strings.Repeat("a", 201)} {
		if ValidEffortPath(invalid) {
			t.Fatalf("ValidEffortPath(%q) = true", invalid)
		}
	}
	if !IsAncestorEffort("feature", "feature.one") {
		t.Fatal("IsAncestorEffort(feature, feature.one) = false")
	}
	if IsAncestorEffort("feature", "feature") || IsAncestorEffort("", "feature.one") || IsAncestorEffort("other", "feature.one") {
		t.Fatal("IsAncestorEffort accepted a non-ancestor")
	}
	if got := ownerAgent("", "agent-1"); got != "agent-1" {
		t.Fatalf("ownerAgent fallback = %q", got)
	}
	if got := ownerAgent("codex", "agent-1"); got != "codex" {
		t.Fatalf("ownerAgent runtime = %q", got)
	}
}

func TestWtLifeCovCheckAdmissionOffShortCircuits(t *testing.T) {
	admission := CheckAdmission(t.TempDir(), AdmissionOff)
	if !admission.Admitted || admission.Reason != "" || admission.Remedy != "" {
		t.Fatalf("AdmissionOff admission = %+v", admission)
	}
}

func TestWtLifeCovJournalDirectoryClassifiesMissingComponents(t *testing.T) {
	if _, err := ReadManifest(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("ReadManifest accepted a missing worktree")
	}

	worktree := wtLifeCovJournalWorktree(t)
	wtLifeCovWriteFile(t, filepath.Join(worktree, journalRootDirectory, journalLocalDirectory), "not a directory\n")
	if _, err := ReadManifest(worktree); err == nil ||
		!strings.Contains(err.Error(), "open work-log journal component local") {
		t.Fatalf("file local error = %v", err)
	}

	journalOnly := wtLifeCovJournalWorktree(t)
	if err := os.MkdirAll(filepath.Join(journalOnly, journalRootDirectory, journalLocalDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(journalOnly); err != errManifestNotFound {
		t.Fatalf("journal without manifest error = %v, want errManifestNotFound", err)
	}
}

func TestWtLifeCovEnsureJournalExcludeReportsUnusableGitInfo(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	info := filepath.Join(worktree, ".git", "info")
	if err := os.RemoveAll(info); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(info, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureJournalExclude(worktree); err == nil || !strings.Contains(err.Error(), "create per-worktree git info") {
		t.Fatalf("unwritable git info error = %v", err)
	}
	if err := WriteManifest(worktree, wtLifeCovValidManifest("feature.one")); err == nil {
		t.Fatal("WriteManifest accepted an unusable git info directory")
	}
	if _, err := ReconstructManifest(context.Background(), worktree); err == nil {
		t.Fatal("ReconstructManifest accepted an unusable git info directory")
	}
	if _, err := PreviewReconstructedManifest(context.Background(), worktree); err != nil {
		t.Fatalf("preview must not persist a manifest: %v", err)
	}
}

func TestWtLifeCovEnsureJournalExcludeReportsUnreadableExclude(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	exclude := filepath.Join(worktree, ".git", "info", "exclude")
	if err := os.RemoveAll(exclude); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(exclude, "blocker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureJournalExclude(worktree); err == nil || !strings.Contains(err.Error(), "read per-worktree exclude") {
		t.Fatalf("directory exclude error = %v", err)
	}
}

func TestWtLifeCovWriteCreationJournalWithoutPrompt(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	result := CreateResult{Repository: "acme/app", WorktreeDir: worktree, Branch: "feature/one"}
	if err := writeCreationJournal("feature.one", "", "", result, WorkLogOptions{AgentRuntime: "codex"}, time.Now().UTC()); err != nil {
		t.Fatalf("writeCreationJournal without prompt: %v", err)
	}
	if _, err := ReadManifest(worktree); err != nil {
		t.Fatalf("creation manifest: %v", err)
	}
	if prompts, err := ListPrompts(worktree); err != nil || prompts != nil {
		t.Fatalf("creation without prompt recorded %v, %v", prompts, err)
	}
}

func TestWtLifeCovReconstructManifestReportsUnusableJournal(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	wtLifeCovWriteFile(t, filepath.Join(worktree, journalRootDirectory), "not a directory\n")
	if _, err := ReconstructManifest(context.Background(), worktree); err == nil {
		t.Fatal("ReconstructManifest accepted a file at .wb")
	}
	if _, err := PreviewReconstructedManifest(context.Background(), worktree); err == nil {
		t.Fatal("PreviewReconstructedManifest accepted a file at .wb")
	}
}

func TestWtLifeCovReconstructManifestInfersBaseFromRemoteTarget(t *testing.T) {
	worktree := wtLifeCovJournalWorktree(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	wtLifeCovGit(t, t.TempDir(), "init", "--bare", "--quiet", "--initial-branch=main", remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	wtLifeCovGit(t, worktree, "remote", "add", "origin", remote)
	wtLifeCovGit(t, worktree, "push", "--quiet", "-u", "origin", "main")
	wtLifeCovGit(t, worktree, "fetch", "--quiet", "origin")

	manifest, err := computeReconstructedManifest(context.Background(), worktree)
	if err != nil {
		t.Fatalf("reconstruction with remote: %v", err)
	}
	if manifest.Base != "main" || len(manifest.BaseSHA) != 40 {
		t.Fatalf("reconstructed base = %q (%q)", manifest.Base, manifest.BaseSHA)
	}
	if !strings.Contains(strings.Join(manifest.InferredFields, ","), "base_sha") {
		t.Fatalf("inferred fields = %v", manifest.InferredFields)
	}
	if !strings.Contains(manifest.Repository, "/") {
		t.Fatalf("reconstructed repository = %q", manifest.Repository)
	}
}

func TestWtLifeCovEffortFromShortPath(t *testing.T) {
	if got := effortFromWorktreePath("/a"); got != "" {
		t.Fatalf("short path effort = %q", got)
	}
}
