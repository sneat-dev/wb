package worktrees

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"filippo.io/age"
)

func TestRetirementPayloadPackVerifyAndPrivacy(t *testing.T) {
	fixture := newGitFixture(t)
	const prompt = "private retirement prompt: glass-orchid\n"
	promptPath := writeWorkLogPromptFile(t, prompt)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retirement-payload", WorkLog: WorkLogOptions{RunID: "retirement-run", Model: "unknown", OriginalPrompt: promptPath, RequireOriginalPrompt: true}})
	if err != nil {
		t.Fatal(err)
	}
	finalizeRetirementPayload(t, fixture.projectsRoot, created[0].WorktreeDir, []byte("final private report\n"))
	expected := retirementPayloadExpectation(t, created[0].WorktreeDir)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := PackRetirementPayload(created[0].WorktreeDir, fixture.home, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if payload.Metadata.FileCount == 0 || payload.Metadata.Retention == "" || payload.Metadata.ClaimID == "" {
		t.Fatalf("metadata = %#v", payload.Metadata)
	}
	if bytes.Contains(payload.Sealed, []byte(prompt)) {
		t.Fatal("sealed payload contains plaintext prompt")
	}
	if err := VerifyRetirementPayload(payload, identity, expected); err != nil {
		t.Fatal(err)
	}
	entries := retirementPayloadEntries(t, payload, identity)
	if string(entries["run/reports/retirement-payload--acme--app.md"]) != "final private report\n" || len(entries["run/locks/"+expected.ClaimID+".lock"]) != 0 {
		t.Fatalf("report or sealed claim lock missing from archive: %#v", entries)
	}
	wrongExpected := expected
	wrongExpected.Worktree = "/independently-expected/different-worktree"
	if err := VerifyRetirementPayload(payload, identity, wrongExpected); err == nil {
		t.Fatal("wrong independent source expectation accepted")
	}

	again, err := PackRetirementPayload(created[0].WorktreeDir, fixture.home, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if payload.Metadata != again.Metadata {
		t.Fatalf("metadata differs: %#v != %#v", payload.Metadata, again.Metadata)
	}
}

func retirementPayloadEntries(t *testing.T, payload RetirementPayload, identity age.Identity) map[string][]byte {
	t.Helper()
	reader, err := age.Decrypt(bytes.NewReader(payload.Sealed), identity)
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewReader(reader)
	entries := map[string][]byte{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name], err = io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
	}
	return entries
}

func TestRetirementPayloadCapturesDirtyEvidenceAndSkipsSiblingClaims(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "private dirty prompt\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retirement-dirty", WorkLog: WorkLogOptions{RunID: "retirement-dirty-run", Model: "unknown", OriginalPrompt: promptPath, RequireOriginalPrompt: true}})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	expected := retirementPayloadExpectation(t, worktree)
	if err := os.WriteFile(filepath.Join(worktree, "private-wip.txt"), []byte("dirty archive bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty, err := captureAndPersistDirtyWorktree(context.Background(), fixture.home, worktree, nil)
	if err != nil {
		t.Fatal(err)
	}
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	if err := sealWorkLogForRecycleWithDirtyCapture(fixture.home, worktree, head, "discarded", dirty); err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Join(fixture.home, "worklogs", expected.EffortID, "runs", expected.RunID)
	if err := os.WriteFile(filepath.Join(runRoot, "claims", "sibling.json"), []byte(`{"claim_id":"sibling"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "locks", "sibling.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := PackRetirementPayload(worktree, fixture.home, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRetirementPayload(payload, identity, expected); err != nil {
		t.Fatal(err)
	}
	entries := retirementPayloadEntries(t, payload, identity)
	if _, ok := entries["run/claims/sibling.json"]; ok {
		t.Fatal("sibling claim leaked into claim-scoped archive")
	}
	manifestName := "run/dirty-discard/" + expected.ClaimID + "/manifest.json"
	var manifest dirtyCaptureManifest
	if err := json.Unmarshal(entries[manifestName], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Receipt != *dirty || len(manifest.Entries) != 1 {
		t.Fatalf("dirty manifest = %#v", manifest)
	}
	blob := "run/dirty-discard/" + expected.ClaimID + "/" + manifest.Entries[0].Blob
	if string(entries[blob]) != "dirty archive bytes\n" {
		t.Fatalf("dirty blob %s = %q", blob, entries[blob])
	}
	entries[blob] = []byte("dirty archive byte!\n")
	forged := sealedRetirementEntries(t, identity.Recipient(), entries, payload.Metadata)
	if err := VerifyRetirementPayload(forged, identity, expected); err == nil || !strings.Contains(err.Error(), "dirty blob digest mismatch") {
		t.Fatalf("forged dirty blob accepted: %v", err)
	}
}

func sealedRetirementEntries(t *testing.T, recipient age.Recipient, entries map[string][]byte, metadata RetirementPayloadMetadata) RetirementPayload {
	t.Helper()
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var plain bytes.Buffer
	writer := tar.NewWriter(&plain)
	for _, name := range names {
		content := entries[name]
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(plain.Bytes())
	metadata.FileCount = len(entries)
	metadata.PlaintextSHA256 = hex.EncodeToString(sum[:])
	var sealed bytes.Buffer
	encryptor, err := age.Encrypt(&sealed, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encryptor.Write(plain.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := encryptor.Close(); err != nil {
		t.Fatal(err)
	}
	return RetirementPayload{Metadata: metadata, Sealed: sealed.Bytes()}
}

func finalizeRetirementPayload(t *testing.T, projectsRoot, worktree string, report []byte) {
	t.Helper()
	if _, err := LogFinalize(context.Background(), LogFinalizeOptions{ProjectsRoot: projectsRoot, Worktree: worktree, Result: "success", Apply: true, Report: report}); err != nil {
		t.Fatal(err)
	}
}

func retirementPayloadExpectation(t *testing.T, worktree string) RetirementPayloadExpectation {
	t.Helper()
	projection, err := readLocalProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(worktree)
	if err != nil {
		t.Fatal(err)
	}
	return RetirementPayloadExpectation{EffortID: projection.EffortID, RunID: projection.RunID, ClaimID: projection.ClaimID, Worktree: worktree, Repository: manifest.Repository}
}

func TestRetirementPayloadRejectsTamperWrongKeyAndUnsafeEntries(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "private prompt\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retirement-unsafe", WorkLog: WorkLogOptions{RunID: "retirement-unsafe-run", Model: "unknown", OriginalPrompt: promptPath, RequireOriginalPrompt: true}})
	if err != nil {
		t.Fatal(err)
	}
	finalizeRetirementPayload(t, fixture.projectsRoot, created[0].WorktreeDir, nil)
	expected := retirementPayloadExpectation(t, created[0].WorktreeDir)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := PackRetirementPayload(created[0].WorktreeDir, fixture.home, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	tampered := payload
	tampered.Sealed = append([]byte(nil), payload.Sealed...)
	tampered.Sealed[len(tampered.Sealed)-1] ^= 1
	if err := VerifyRetirementPayload(tampered, identity, expected); err == nil {
		t.Fatal("tampered payload accepted")
	}
	wrong, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRetirementPayload(payload, wrong, expected); err == nil {
		t.Fatal("wrong identity accepted")
	}

	unsafe := sealedTestPayload(t, identity.Recipient(), "../escape", []byte("x"), payload.Metadata)
	if err := VerifyRetirementPayload(unsafe, identity, expected); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("traversal error = %v", err)
	}
	duplicate := sealedTestPayload(t, identity.Recipient(), "journal/manifest.yaml", []byte("x"), payload.Metadata, "journal/manifest.yaml")
	if err := VerifyRetirementPayload(duplicate, identity, expected); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func sealedTestPayload(t *testing.T, recipient age.Recipient, name string, content []byte, metadata RetirementPayloadMetadata, duplicate ...string) RetirementPayload {
	t.Helper()
	var plain bytes.Buffer
	tw := tar.NewWriter(&plain)
	for _, entry := range append([]string{name}, duplicate...) {
		if err := tw.WriteHeader(&tar.Header{Name: entry, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(plain.Bytes())
	metadata.FileCount = 1 + len(duplicate)
	metadata.PlaintextSHA256 = hex.EncodeToString(sum[:])
	var sealed bytes.Buffer
	writer, err := age.Encrypt(&sealed, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(plain.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return RetirementPayload{Metadata: metadata, Sealed: sealed.Bytes()}
}

func TestRetirementPayloadRejectsJournalSymlink(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "private prompt\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retirement-link", WorkLog: WorkLogOptions{RunID: "retirement-link-run", Model: "unknown", OriginalPrompt: promptPath, RequireOriginalPrompt: true}})
	if err != nil {
		t.Fatal(err)
	}
	finalizeRetirementPayload(t, fixture.projectsRoot, created[0].WorktreeDir, nil)
	if err := os.Symlink("/tmp", filepath.Join(created[0].WorktreeDir, ".wb", "local", "escape")); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PackRetirementPayload(created[0].WorktreeDir, fixture.home, identity.Recipient()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestRetirementPayloadRejectsUnallowlistedJournalFile(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "private prompt\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retirement-unallowlisted", WorkLog: WorkLogOptions{RunID: "retirement-unallowlisted-run", Model: "unknown", OriginalPrompt: promptPath, RequireOriginalPrompt: true}})
	if err != nil {
		t.Fatal(err)
	}
	finalizeRetirementPayload(t, fixture.projectsRoot, created[0].WorktreeDir, nil)
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, ".wb", "local", "surprise.txt"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PackRetirementPayload(created[0].WorktreeDir, fixture.home, identity.Recipient()); err == nil || !strings.Contains(err.Error(), "unallowlisted") {
		t.Fatalf("unallowlisted file error = %v", err)
	}
}

func TestRetirementPayloadRequiresTerminalAndRejectsRunSymlinkAncestor(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "private prompt\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retirement-ancestor", WorkLog: WorkLogOptions{RunID: "retirement-ancestor-run", Model: "unknown", OriginalPrompt: promptPath, RequireOriginalPrompt: true}})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if _, err := PackRetirementPayload(worktree, fixture.home, identity.Recipient()); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("unterminated claim accepted: %v", err)
	}
	finalizeRetirementPayload(t, fixture.projectsRoot, worktree, []byte("private report\n"))
	expected := retirementPayloadExpectation(t, worktree)
	reports := filepath.Join(fixture.home, "worklogs", expected.EffortID, "runs", expected.RunID, "reports")
	outside := filepath.Join(t.TempDir(), "reports")
	if err := os.Rename(reports, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, reports); err != nil {
		t.Fatal(err)
	}
	if _, err := PackRetirementPayload(worktree, fixture.home, identity.Recipient()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked report ancestor accepted: %v", err)
	}
}

func TestRetirementPayloadRejectsOversizeSourceBeforeEncryption(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "private prompt\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retirement-oversize", WorkLog: WorkLogOptions{RunID: "retirement-oversize-run", Model: "unknown", OriginalPrompt: promptPath, RequireOriginalPrompt: true}})
	if err != nil {
		t.Fatal(err)
	}
	finalizeRetirementPayload(t, fixture.projectsRoot, created[0].WorktreeDir, nil)
	expected := retirementPayloadExpectation(t, created[0].WorktreeDir)
	original := filepath.Join(fixture.home, "worklogs", expected.EffortID, "runs", expected.RunID, "original-prompt.txt")
	if err := os.Truncate(original, retirementMaxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PackRetirementPayload(created[0].WorktreeDir, fixture.home, identity.Recipient()); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversize source accepted: %v", err)
	}
}
