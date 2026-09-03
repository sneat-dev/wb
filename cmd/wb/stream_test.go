package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

// A refusal must be distinguishable from a failure without parsing prose: it
// exits 2 and its JSON envelope carries the stable refusal code and the exact
// sanctioned command.
//
// Requirements: dependency-streams#req:verbs-share-an-exit-code-and-envelope-contract,
// dependency-streams#req:every-refusal-names-the-sanctioned-command.
func TestStreamStartRefusalExitsUsageWithItsEnvelope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	store := streams.OpenAt(filepath.Join(home, "streams"))
	if _, err := store.Create(streams.Stream{
		Name:    "holder",
		Members: []streams.Member{{Repository: "acme/app", Role: streams.RoleConsumer}},
	}); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("the exact task request\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"stream", "start", "second", "acme/app",
		"--mode", "manual", "--initiator", "me@example.com", "--model", "unknown",
		"--original-prompt-file", prompt,
		"--format", "json", "--non-interactive",
	}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d (refusal); stderr=%s", code, exitUsage, stderr.String())
	}
	var envelope struct {
		Version           int               `json:"v"`
		Verb              string            `json:"verb"`
		Outcome           string            `json:"outcome"`
		RefusalCode       string            `json:"refusal_code"`
		SanctionedCommand string            `json:"sanctioned_command"`
		Evidence          map[string]string `json:"evidence"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("parse envelope from %q: %v", stdout.String(), err)
	}
	if envelope.Version != 1 || envelope.Verb != "stream start" || envelope.Outcome != "refused" {
		t.Fatalf("envelope = %#v", envelope)
	}
	if envelope.RefusalCode != streams.RefusalRepositoryInStream {
		t.Errorf("refusal_code = %q, want %q", envelope.RefusalCode, streams.RefusalRepositoryInStream)
	}
	if !strings.Contains(envelope.SanctionedCommand, "wb stream join holder acme/app") {
		t.Errorf("sanctioned_command = %q, want the join command", envelope.SanctionedCommand)
	}
	if !strings.Contains(envelope.Evidence["message"], "holder") {
		t.Errorf("evidence = %#v, want the holding stream named", envelope.Evidence)
	}
}

// `wb stream status` with no name lists every stream from WB-owned state, and
// the JSON document on stdout stays parseable.
func TestStreamStatusListsStreamsFromWBOwnedState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	store := streams.OpenAt(filepath.Join(home, "streams"))
	if _, err := store.Create(streams.Stream{
		Name:    "listed",
		Members: []streams.Member{{Repository: "acme/library", Role: streams.RoleLibrary}},
	}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"stream", "status", "--format", "json", "--non-interactive"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr.String())
	}
	var envelope struct {
		Verb     string `json:"verb"`
		Outcome  string `json:"outcome"`
		Evidence struct {
			Streams []struct {
				Name string `json:"name"`
			} `json:"streams"`
			Unreadable []struct {
				Name string `json:"name"`
			} `json:"unreadable"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("parse envelope from %q: %v", stdout.String(), err)
	}
	if envelope.Outcome != "success" || len(envelope.Evidence.Streams) != 1 || envelope.Evidence.Streams[0].Name != "listed" {
		t.Fatalf("envelope = %#v", envelope)
	}
	var textOut, textErr bytes.Buffer
	if code := run([]string{"stream", "status", "--non-interactive"}, &textOut, &textErr); code != exitOK {
		t.Fatalf("text exit code = %d; stderr=%s", code, textErr.String())
	}
	if !strings.Contains(textOut.String(), "listed") {
		t.Errorf("text output = %q, want the stream named", textOut.String())
	}
}

// A stream name that could not also be a worktree task name is rejected before
// anything durable is created.
func TestStreamStartRejectsAnInvalidName(t *testing.T) {
	t.Setenv("WB_HOME", t.TempDir())
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"stream", "start", "not a name", "acme/app",
		"--mode", "manual", "--initiator", "me@example.com", "--model", "unknown",
		"--original-prompt-file", prompt, "--format", "json", "--non-interactive",
	}, &stdout, &stderr)
	// An ambiguous invocation is exit 2, not exit 1: 1 means "the work is
	// broken", and a caller that branches on the contract must be able to
	// tell a bad invocation from a real failure.
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d (usage); stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "must start with a letter or digit") {
		t.Errorf("stderr = %q, want the name rule", stderr.String())
	}
	// A caller that asked for JSON must never get an empty stdout.
	var envelope struct {
		Verb        string `json:"verb"`
		Outcome     string `json:"outcome"`
		RefusalCode string `json:"refusal_code"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("parse envelope from %q: %v", stdout.String(), err)
	}
	if envelope.Outcome != "refused" || envelope.RefusalCode != streams.RefusalUsage {
		t.Fatalf("envelope = %#v, want a usage refusal", envelope)
	}
}

// An unsupported --role is the same contract: exit 2 with an envelope naming
// the sanctioned invocation.
func TestStreamJoinRejectsAnUnsupportedRoleWithTheUsageEnvelope(t *testing.T) {
	t.Setenv("WB_HOME", t.TempDir())
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"stream", "join", "somename", "acme/app", "--role", "bogus",
		"--mode", "manual", "--initiator", "me@example.com", "--model", "unknown",
		"--original-prompt-file", prompt, "--format", "json", "--non-interactive",
	}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitUsage, stderr.String())
	}
	var envelope struct {
		RefusalCode        string   `json:"refusal_code"`
		SanctionedCommand  string   `json:"sanctioned_command"`
		SanctionedCommands []string `json:"sanctioned_commands"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("parse envelope from %q: %v", stdout.String(), err)
	}
	if envelope.RefusalCode != streams.RefusalUsage {
		t.Fatalf("refusal_code = %q", envelope.RefusalCode)
	}
	// The singular field must be one runnable command, not a joined string.
	if !strings.HasPrefix(envelope.SanctionedCommand, "wb stream join") || strings.Contains(envelope.SanctionedCommand, "||") {
		t.Errorf("sanctioned_command = %q, want one runnable command", envelope.SanctionedCommand)
	}
	if len(envelope.SanctionedCommands) == 0 {
		t.Error("sanctioned_commands is empty")
	}
}

// `wb stream delete` refuses an open stream and names the command that makes
// it deletable.
func TestStreamDeleteRefusesAnOpenStream(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	store := streams.OpenAt(filepath.Join(home, "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "held", Phase: streams.PhaseOpen,
		Members: []streams.Member{{Repository: "acme/app", Role: streams.RoleConsumer}},
	}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"stream", "delete", "held", "--format", "json", "--non-interactive"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stdout.String(), "wb stream end held --apply") {
		t.Errorf("envelope does not name the sanctioned command: %s", stdout.String())
	}
}

// `wb stream end` on a stream holding a live link refuses with exit 2 and
// names the exact undo command, so an agent never has to hand-chain git to
// clear a link.
func TestStreamEndRefusesALiveLinkAndNamesTheUndoCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	store := streams.OpenAt(filepath.Join(home, "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "linked",
		Members: []streams.Member{{
			Repository: "acme/app", Role: streams.RoleConsumer, Worktree: "/tmp/app",
			Links: []streams.Link{{
				Library: "/tmp/library", LibraryRepository: "acme/library",
				Mechanism: streams.MechanismGoWork, Identity: "github.com/acme/library/backend",
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"stream", "end", "linked", "--apply", "--format", "json", "--non-interactive"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stdout.String(), streams.RefusalLiveLink) {
		t.Errorf("envelope = %q, want the live-link refusal code", stdout.String())
	}
	if !strings.Contains(stdout.String(), "wb deps propagate local /tmp/library --to /tmp/app --undo") {
		t.Errorf("envelope = %q, want the exact undo command", stdout.String())
	}
}
