package worktrees

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/sessionpark"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// TestBatch7BoundedRelativeRegularFailureBoundaries proves the descriptor
// validation failures that cannot be scheduled reliably with filesystem races.
func TestBatch7BoundedRelativeRegularFailureBoundaries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "handover.md"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalFstat, originalReadAll := readBoundedRelativeRegularFstat, readBoundedRelativeRegularReadAll
	t.Cleanup(func() {
		readBoundedRelativeRegularFstat = originalFstat
		readBoundedRelativeRegularReadAll = originalReadAll
	})

	readBoundedRelativeRegularFstat = func(int, *unix.Stat_t) error { return errors.New("injected fstat failure") }
	if _, err := readBoundedRelativeRegular(root, "handover.md", 64); err == nil || !strings.Contains(err.Error(), "injected fstat failure") {
		t.Fatalf("fstat failure = %v", err)
	}
	readBoundedRelativeRegularFstat = originalFstat
	readBoundedRelativeRegularReadAll = func(io.Reader) ([]byte, error) { return nil, errors.New("injected read failure") }
	if _, err := readBoundedRelativeRegular(root, "handover.md", 64); err == nil || !strings.Contains(err.Error(), "injected read failure") {
		t.Fatalf("read failure = %v", err)
	}
	readBoundedRelativeRegularReadAll = func(io.Reader) ([]byte, error) { return []byte("changed"), nil }
	if _, err := readBoundedRelativeRegular(root, "handover.md", 64); err == nil || !strings.Contains(err.Error(), "changed while being read") {
		t.Fatalf("drift failure = %v", err)
	}
}

func TestBatch7ExternalHandoverPromptRejectsUnusableSequence(t *testing.T) {
	root := t.TempDir()
	prompts := filepath.Join(root, journalRootDirectory, journalLocalDirectory, promptsDirectory)
	if err := os.MkdirAll(prompts, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"0001-second.md": "---\nseq: 1\nsource: agent_observed\n---\n\nbody\n",
		"notes.md":       "not a prompt",
	} {
		if err := os.WriteFile(filepath.Join(prompts, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateExternalHandoverPrompt(root, time.Time{}, "", "", "", nil); err == nil {
		t.Fatal("non-contiguous one-prompt sequence was accepted")
	}
}

func TestBatch7SessionReceiveCanonicalAndHeldRootBoundaries(t *testing.T) {
	if _, err := openSessionReceiveCanonicalFromHeldRoot(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "descriptor is unavailable") {
		t.Fatalf("nil held root error = %v", err)
	}
	fixture := newSessionReceiveFixture(t)
	canonical := mustOpenCanonical(t, fixture.canonical)
	defer canonical.close()
	declared, err := gitremote.Parse(fixture.remote)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySessionReceiveCanonical(context.Background(), canonical, declared.Identity); err != nil {
		t.Fatalf("valid canonical rejected: %v", err)
	}
	ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
		if len(args) >= 1 && args[0] == "rev-parse" {
			return []byte(filepath.Join(fixture.root, "replacement")), nil
		}
		return run()
	})
	if err := verifySessionReceiveCanonical(ctx, canonical, declared.Identity); err == nil || !strings.Contains(err.Error(), "not the root") {
		t.Fatalf("canonical replacement error = %v", err)
	}
}

func TestBatch7RetireAndRemoteCustodyGuards(t *testing.T) {
	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	if err := retireCompletedInterruptedSessionStage(context.Background(), root, directory); err != nil {
		t.Fatalf("empty interrupted stage retirement: %v", err)
	}
	for _, suffix := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(root, ".wb-stage-"+strings.Repeat(suffix, 32)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := retireCompletedInterruptedSessionStage(context.Background(), root, directory); err == nil || !strings.Contains(err.Error(), "multiple active") {
		t.Fatalf("ambiguous stage retirement = %v", err)
	}
	if err := WithParkedRemoteResumeCustody(context.Background(), t.TempDir(), sessionpark.Bundle{}, nil); err == nil {
		t.Fatal("nil remote custody callback was accepted")
	}
	if err := WithParkedRemoteResumeCustody(context.Background(), t.TempDir(), sessionpark.Bundle{Worktrees: make([]sessionpark.Worktree, sessionpark.MaxMembers+1)}, func() error { return nil }); err == nil {
		t.Fatal("oversized remote custody bundle was accepted")
	}
}
