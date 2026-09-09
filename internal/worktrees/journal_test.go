package worktrees

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newJournalWorktree(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(root, "checkout")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "init")
	// CI runners carry no global Git identity, so a fixture that commits must
	// supply its own rather than inheriting the developer's.
	gitTest(t, worktree, "config", "user.name", "WB Test")
	gitTest(t, worktree, "config", "user.email", "wb@example.test")
	return worktree
}

func newCreatedManifest(effort string) Manifest {
	return Manifest{
		Version:      1,
		EffortID:     effort,
		ParentEffort: ParentEffort(effort),
		EffortKind:   EffortKindFor(effort),
		Repository:   "sneat-co/sneat-bots",
		Worktree:     "/tmp/checkout",
		Branch:       effort,
		Base:         "main",
		BaseSHA:      "0123456789abcdef",
		CreatedAt:    time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC),
		Provenance:   ProvenanceCreated,
	}
}

func TestEffortFromWorktreePathSupportsLocalAndSharedLayouts(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "local", path: "/projects/acme/app/.worktrees/fair-split", want: "fair-split"},
		{name: "shared", path: "/shared/worktrees/fair-split/acme/app", want: "fair-split"},
		{name: "shared", path: "/shared/fair-split/acme/app", want: "fair-split"},
		{name: "invalid local effort", path: "/projects/acme/app/.worktrees/not a task", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := effortFromWorktreePath(test.path); got != test.want {
				t.Fatalf("effortFromWorktreePath(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

// The whole point of holding the journal in the worktree is that an orphan can
// be triaged with nothing else intact. This deletes every external record
// before reading, because that is the state an abandoned checkout is found in.
func TestManifestAndPromptsSurviveLossOfEveryExternalRecord(t *testing.T) {
	worktree := newJournalWorktree(t)
	if err := WriteManifest(worktree, newCreatedManifest("gg-input-types")); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendPrompt(worktree, PromptHeader{Source: PromptSourceHuman}, []byte("prevent commits without a manifest")); err != nil {
		t.Fatal(err)
	}

	gitDir, err := git(t.Context(), worktree, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gitDir); err != nil {
		t.Fatal(err)
	}

	manifest, err := ReadManifest(worktree)
	if err != nil {
		t.Fatalf("manifest must be readable without any external record: %v", err)
	}
	if manifest.EffortID != "gg-input-types" || manifest.Provenance != ProvenanceCreated {
		t.Fatalf("unexpected recovered manifest: %+v", manifest)
	}
	prompts, err := ListPrompts(worktree)
	if err != nil {
		t.Fatalf("prompts must be readable without any external record: %v", err)
	}
	if len(prompts) != 1 || prompts[0].Seq != 0 || prompts[0].Source != PromptSourceHuman {
		t.Fatalf("unexpected recovered prompts: %+v", prompts)
	}
}

func TestManifestIsImmutableOnceWritten(t *testing.T) {
	worktree := newJournalWorktree(t)
	if err := WriteManifest(worktree, newCreatedManifest("effort")); err != nil {
		t.Fatal(err)
	}
	err := WriteManifest(worktree, newCreatedManifest("effort"))
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("a second manifest write must be refused, got %v", err)
	}
}

func TestConcurrentManifestWritersCannotReplaceWinner(t *testing.T) {
	worktree := newJournalWorktree(t)
	manifests := []Manifest{newCreatedManifest("first-effort"), newCreatedManifest("second-effort")}
	start := make(chan struct{})
	errorsByWriter := make([]error, len(manifests))
	var wait sync.WaitGroup
	for index := range manifests {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			errorsByWriter[index] = WriteManifest(worktree, manifests[index])
		}(index)
	}
	close(start)
	wait.Wait()
	winner := -1
	for index, err := range errorsByWriter {
		if err == nil {
			if winner >= 0 {
				t.Fatalf("both conflicting manifest writers succeeded: errors=%#v", errorsByWriter)
			}
			winner = index
		}
	}
	if winner < 0 {
		t.Fatalf("neither manifest writer won: errors=%#v", errorsByWriter)
	}
	stored, err := ReadManifest(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EffortID != manifests[winner].EffortID {
		t.Fatalf("stored manifest=%#v, want exact winner %#v", stored, manifests[winner])
	}
}

func TestReconstructedManifestMustRecordWhatWasInferred(t *testing.T) {
	worktree := newJournalWorktree(t)
	manifest := newCreatedManifest("legacy-effort")
	manifest.Provenance = ProvenanceReconstructed
	if err := WriteManifest(worktree, manifest); err == nil {
		t.Fatal("a reconstructed manifest without inferred fields must be refused")
	}
	manifest.InferredFields = []string{"base_sha"}
	manifest.Evidence = []string{"first reflog entry"}
	if err := WriteManifest(worktree, manifest); err != nil {
		t.Fatalf("a reconstructed manifest recording its evidence is valid: %v", err)
	}
	recovered, err := ReadManifest(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Provenance != ProvenanceReconstructed || len(recovered.InferredFields) != 1 {
		t.Fatalf("inference must stay distinguishable from a creation record: %+v", recovered)
	}
}

// Lexical order must equal chronological order past ordinal nine, which is
// exactly where an unpadded ordinal starts replaying a session out of order.
func TestPromptOrdinalsStayOrderedPastNine(t *testing.T) {
	worktree := newJournalWorktree(t)
	for index := 0; index < 12; index++ {
		body := []byte("instruction " + string(rune('a'+index)))
		if _, err := AppendPrompt(worktree, PromptHeader{Source: PromptSourceAgent}, body); err != nil {
			t.Fatal(err)
		}
	}
	names, err := os.ReadDir(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory))
	if err != nil {
		t.Fatal(err)
	}
	promptNames := make([]os.DirEntry, 0, len(names))
	for _, name := range names {
		if promptFileName.MatchString(name.Name()) {
			promptNames = append(promptNames, name)
		}
	}
	if len(promptNames) != 12 {
		t.Fatalf("expected 12 prompts, got %d", len(promptNames))
	}
	if !strings.HasPrefix(promptNames[0].Name(), "0000-") || !strings.HasPrefix(promptNames[10].Name(), "0010-") {
		t.Fatalf("directory order must equal ordinal order: %s ... %s", promptNames[0].Name(), promptNames[10].Name())
	}
	prompts, err := ListPrompts(worktree)
	if err != nil {
		t.Fatal(err)
	}
	for index, prompt := range prompts {
		if prompt.Seq != index {
			t.Fatalf("prompt %d records seq %d", index, prompt.Seq)
		}
	}
}

func TestConcurrentPromptWritersSerializeOrdinalsWithoutOverwrite(t *testing.T) {
	worktree := newJournalWorktree(t)
	bodies := [][]byte{[]byte("first exact concurrent prompt\n"), []byte("second exact concurrent prompt\n")}
	start := make(chan struct{})
	names := make([]string, len(bodies))
	errorsByWriter := make([]error, len(bodies))
	var wait sync.WaitGroup
	for index := range bodies {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			names[index], errorsByWriter[index] = AppendPrompt(worktree, PromptHeader{
				Source: PromptSourceAgent, Slug: "same-slug", At: time.Date(2026, 8, 25, 12, 0, index, 0, time.UTC),
			}, bodies[index])
		}(index)
	}
	close(start)
	wait.Wait()
	for index, err := range errorsByWriter {
		if err != nil {
			t.Fatalf("prompt writer %d: %v", index, err)
		}
	}
	if names[0] == names[1] {
		t.Fatalf("concurrent prompts selected one replaceable path %q", names[0])
	}
	prompts, err := ListPrompts(worktree)
	if err != nil || len(prompts) != 2 || prompts[0].Seq != 0 || prompts[1].Seq != 1 {
		t.Fatalf("concurrent prompt sequence=%#v err=%v", prompts, err)
	}
	for index, name := range names {
		stored, err := os.ReadFile(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory, name))
		if err != nil || !strings.HasSuffix(string(stored), string(bodies[index])) {
			t.Fatalf("prompt %d path=%q bytes=%q err=%v", index, name, stored, err)
		}
	}
}

func TestPromptSourceIsRecordedNeverInferred(t *testing.T) {
	worktree := newJournalWorktree(t)
	if _, err := AppendPrompt(worktree, PromptHeader{}, []byte("no source")); err == nil {
		t.Fatal("a prompt without an explicit source must be refused")
	}
	if _, err := AppendPrompt(worktree, PromptHeader{Source: "guessed"}, []byte("bad source")); err == nil {
		t.Fatal("an unrecognized prompt source must be refused")
	}
	for _, source := range []string{PromptSourceHarness, PromptSourceAgent, PromptSourceHuman} {
		if _, err := AppendPrompt(worktree, PromptHeader{Source: source}, []byte("body for "+source)); err != nil {
			t.Fatalf("source %s must be accepted: %v", source, err)
		}
	}
}

func TestPromptBodyIsStoredExactlyWithItsDigest(t *testing.T) {
	worktree := newJournalWorktree(t)
	body := []byte("worklog can't be 20 bytes - worklog is all what happened\n")
	name, err := AppendPrompt(worktree, PromptHeader{Source: PromptSourceHuman}, body)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory, name))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(content), string(body)) {
		t.Fatalf("prompt body must be stored exactly, got %q", content)
	}
	prompts, err := ListPrompts(worktree)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	if prompts[0].SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("prompt digest must be the sha256 of the exact body, got %q", prompts[0].SHA256)
	}
}

// The exclude rule must never be /.wb/, which would swallow newly added files
// in the repository's own tracked .wb/templates/.
func TestExcludeRuleDoesNotSwallowTrackedRepositoryPolicy(t *testing.T) {
	worktree := newJournalWorktree(t)
	policy := filepath.Join(worktree, journalRootDirectory, "templates")
	if err := os.MkdirAll(policy, 0o755); err != nil {
		t.Fatal(err)
	}
	newPolicyFile := filepath.Join(policy, "repo-pre-commit.sh")
	if err := os.WriteFile(newPolicyFile, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(worktree, newCreatedManifest("effort")); err != nil {
		t.Fatal(err)
	}
	ignored, err := git(t.Context(), worktree, "check-ignore", "-q", ".wb/templates/repo-pre-commit.sh")
	if err == nil {
		t.Fatalf("a repository's own tracked policy file must not be ignored (got %q)", ignored)
	}
	if _, err := git(t.Context(), worktree, "check-ignore", "-q", ".wb/local/manifest.yaml"); err != nil {
		t.Fatalf("the journal must be excluded: %v", err)
	}
}

func TestEffortPathParentageIsLexicalAndValidated(t *testing.T) {
	for _, valid := range []string{"feature", "feature.task", "feature.task1.subtask2.level4"} {
		if !ValidEffortPath(valid) {
			t.Fatalf("%q must be a valid effort path", valid)
		}
	}
	for _, invalid := range []string{"", ".", ".leading", "trailing.", "a..b", strings.Repeat("x", 201)} {
		if ValidEffortPath(invalid) {
			t.Fatalf("%q must be rejected", invalid)
		}
	}
	if got := ParentEffort("a.b.c"); got != "a.b" {
		t.Fatalf("parent of a.b.c is a.b, got %q", got)
	}
	if got := ParentEffort("root"); got != "" {
		t.Fatalf("a root effort has no parent, got %q", got)
	}
	if EffortKindFor("root") != EffortKindFeature || EffortKindFor("root.task") != EffortKindTask {
		t.Fatal("a nested effort is a task effort, a root effort is a feature effort")
	}
	if !IsAncestorEffort("a", "a.b.c") || IsAncestorEffort("a.b", "a.bc") {
		t.Fatal("ancestry must match whole segments, not string prefixes")
	}
}

func TestManifestRejectsParentContradictingItsPath(t *testing.T) {
	worktree := newJournalWorktree(t)
	manifest := newCreatedManifest("feature.task")
	manifest.ParentEffort = "somewhere-else"
	err := WriteManifest(worktree, manifest)
	if err == nil || !strings.Contains(err.Error(), "contradicts") {
		t.Fatalf("a manifest parent contradicting the path must be refused, got %v", err)
	}
}

// Warn must never refuse, and enforce must refuse for both reasons a worktree
// can lack its record. Warn mode is what lets a fleet with unattended agents
// adopt the gate without a flag day.
func TestAdmissionWarnsBeforeItEnforces(t *testing.T) {
	worktree := newJournalWorktree(t)

	warned := CheckAdmission(worktree, AdmissionWarn)
	if !warned.Admitted || warned.Reason == "" || warned.Remedy == "" {
		t.Fatalf("warn must report without refusing: %+v", warned)
	}
	refused := CheckAdmission(worktree, AdmissionEnforce)
	if refused.Admitted || !strings.Contains(refused.Remedy, "wb worktree set --prompt") {
		t.Fatalf("enforce must refuse and name the remedy: %+v", refused)
	}
	if off := CheckAdmission(worktree, AdmissionOff); !off.Admitted || off.Reason != "" {
		t.Fatalf("off must not evaluate the journal at all: %+v", off)
	}

	// A manifest alone is not enough: a commit still needs a recorded
	// instruction, otherwise nothing says who directed it.
	if err := WriteManifest(worktree, newCreatedManifest("effort")); err != nil {
		t.Fatal(err)
	}
	stillRefused := CheckAdmission(worktree, AdmissionEnforce)
	if stillRefused.Admitted || !strings.Contains(stillRefused.Reason, "no recorded instruction") {
		t.Fatalf("a manifest without a prompt must still be refused: %+v", stillRefused)
	}

	if _, err := AppendPrompt(worktree, PromptHeader{Source: PromptSourceHuman}, []byte("do the thing")); err != nil {
		t.Fatal(err)
	}
	if admitted := CheckAdmission(worktree, AdmissionEnforce); !admitted.Admitted {
		t.Fatalf("recording an instruction must unblock the commit: %+v", admitted)
	}
}

// The remedy the gate names must be sufficient on its own. A worktree with
// neither a manifest nor a prompt is reported for the manifest first, so
// recording a prompt has to backfill the manifest too or the advice is a dead
// end.
func TestReconstructionMakesTheNamedRemedySufficient(t *testing.T) {
	worktree := newJournalWorktree(t)
	gitTest(t, worktree, "commit", "--allow-empty", "-m", "seed")
	gitTest(t, worktree, "checkout", "-b", "some-effort")

	if CheckAdmission(worktree, AdmissionEnforce).Admitted {
		t.Fatal("a journal-less worktree must be refused first")
	}
	manifest, err := ReconstructManifest(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Provenance != ProvenanceReconstructed || len(manifest.InferredFields) == 0 {
		t.Fatalf("reconstruction must label itself and its inferences: %+v", manifest)
	}
	if manifest.Branch != "some-effort" {
		t.Fatalf("reconstruction must recover the real branch, got %q", manifest.Branch)
	}
	// Reconstruction must never invent an instruction.
	prompts, err := ListPrompts(worktree)
	if err != nil || len(prompts) != 0 {
		t.Fatalf("reconstruction must not fabricate a prompt, got %+v (%v)", prompts, err)
	}
	if _, err := AppendPrompt(worktree, PromptHeader{Source: PromptSourceHuman}, []byte("carry on")); err != nil {
		t.Fatal(err)
	}
	if !CheckAdmission(worktree, AdmissionEnforce).Admitted {
		t.Fatal("the named remedy must be sufficient to unblock the commit")
	}
}

func TestReconstructionIsIdempotentAndNeverOverwrites(t *testing.T) {
	worktree := newJournalWorktree(t)
	gitTest(t, worktree, "commit", "--allow-empty", "-m", "seed")
	gitTest(t, worktree, "checkout", "-b", "some-effort")

	first, err := ReconstructManifest(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ReconstructManifest(t.Context(), worktree)
	if err != nil {
		t.Fatalf("reconstruction must be safely re-runnable: %v", err)
	}
	if first.CreatedAt != second.CreatedAt || first.EffortID != second.EffortID {
		t.Fatalf("a second reconstruction must return the existing record, not a new one:\n%+v\n%+v", first, second)
	}
}

func TestMissingManifestIsADeterministicDiagnosis(t *testing.T) {
	worktree := newJournalWorktree(t)
	_, err := ReadManifest(worktree)
	if !errors.Is(err, errManifestNotFound) {
		t.Fatalf("a worktree with no journal must report a specific diagnosis, got %v", err)
	}
	prompts, err := ListPrompts(worktree)
	if err != nil || len(prompts) != 0 {
		t.Fatalf("a worktree with no prompts lists none without error, got %v / %v", prompts, err)
	}
}
