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
	root := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", root)
	home := filepath.Join(root, ".wb")
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

// Every existing "stream join" test in this package is refused before the
// stream engine is ever built (a bad name, a bad role, a missing --model).
// This drives a join whose work-log preparation and role succeed, so it
// reaches the native engine through newStreamService, proving inv.projectsRoot threads all the
// way into the engine's Store/Git/GitHub/Login/Machine wiring.
func TestStreamJoinReachesTheStreamEngine(t *testing.T) {
	root := t.TempDir()
	// WB_PROJECTS_ROOT is deliberately pointed at an empty decoy directory,
	// not at root: wbhome falls back to this env var for an empty
	// inv.projectsRoot (internal/wbhome/wbhome.go), so pointing it at root
	// too would make a dropped/empty invocation (mutation M20) resolve the
	// same fixture store by accident and this test would never notice.
	t.Setenv("WB_PROJECTS_ROOT", filepath.Join(t.TempDir(), "decoy-not-the-fixture-root"))
	home := filepath.Join(root, ".wb")
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
		"stream", "join", "holder", "acme/newmember",
		"--projects-root", root,
		"--mode", "manual", "--initiator", "me@example.com", "--model", "unknown",
		"--original-prompt-file", prompt,
		"--format", "json", "--non-interactive",
	}, &stdout, &stderr)
	// Whatever the engine's own Join outcome is (it may still refuse for a
	// repository reason unrelated to work-log preparation or role), the
	// refusal must not be one of the pre-engine usage checks: those would
	// mean the native engine binding was never reached.
	if strings.Contains(stderr.String(), "--model is required") || strings.Contains(stderr.String(), "stream name") || strings.Contains(stderr.String(), "unsupported role") {
		t.Fatalf("stream join failed before reaching the stream engine: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	// A positive, root-dependent outcome: the engine must have found the
	// "holder" stream this test seeded under root's own .wb/streams. A wrong
	// or dropped inv.projectsRoot (e.g. constructing the stream service with
	// &invocation{}) resolves an empty/real-home store instead, where
	// "holder" does not exist, and Join fails with streams.ErrNotFound
	// ("stream not found") rather than any of the refusals above -
	// mutation M20 (sneat-dev/wb#760 review B5).
	if strings.Contains(stderr.String(), "stream not found") {
		t.Fatalf("stream join could not find the fixture's own \"holder\" stream, so it did not use root's .wb/streams: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

// `wb stream status` with no name lists every stream from WB-owned state, and
// the JSON document on stdout stays parseable.
func TestStreamStatusListsStreamsFromWBOwnedState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", root)
	home := filepath.Join(root, ".wb")
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

// A missing member pull request is an actionable stream defect, not merely a
// buried persistence field. Status must fail with findings and print the WB
// verb that safely retries publication.

// A stream name that could not also be a worktree task name is rejected before
// anything durable is created.

// An unsupported --role is the same contract: exit 2 with an envelope naming
// the sanctioned invocation.

// `wb stream delete` refuses an open stream and names the command that makes
// it deletable.
func TestStreamDeleteRefusesAnOpenStream(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", root)
	home := filepath.Join(root, ".wb")
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
	root := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", root)
	home := filepath.Join(root, ".wb")
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
