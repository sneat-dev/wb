package worktrees

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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
	if err := VerifyRetirementPayload(payload, identity); err != nil {
		t.Fatal(err)
	}

	again, err := PackRetirementPayload(created[0].WorktreeDir, fixture.home, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if payload.Metadata != again.Metadata {
		t.Fatalf("metadata differs: %#v != %#v", payload.Metadata, again.Metadata)
	}
}

func TestRetirementPayloadRejectsTamperWrongKeyAndUnsafeEntries(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "private prompt\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retirement-unsafe", WorkLog: WorkLogOptions{RunID: "retirement-unsafe-run", Model: "unknown", OriginalPrompt: promptPath, RequireOriginalPrompt: true}})
	if err != nil {
		t.Fatal(err)
	}
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
	if err := VerifyRetirementPayload(tampered, identity); err == nil {
		t.Fatal("tampered payload accepted")
	}
	wrong, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRetirementPayload(payload, wrong); err == nil {
		t.Fatal("wrong identity accepted")
	}

	unsafe := sealedTestPayload(t, identity.Recipient(), "../escape", []byte("x"), payload.Metadata)
	if err := VerifyRetirementPayload(unsafe, identity); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("traversal error = %v", err)
	}
	duplicate := sealedTestPayload(t, identity.Recipient(), "journal/manifest.yaml", []byte("x"), payload.Metadata, "journal/manifest.yaml")
	if err := VerifyRetirementPayload(duplicate, identity); err == nil || !strings.Contains(err.Error(), "duplicate") {
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
