//go:build e2e

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
)

type collaborationActorRequest struct {
	Binary string   `json:"binary"`
	Args   []string `json:"args"`
}

type collaborationActorResult struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// TestE2ECollaborationPeerActor is a persistent registered ancestor of real
// wb subprocesses. Each actor has its own PID, unlike re-registering one test
// process under two WB session IDs.
func TestE2ECollaborationPeerActor(t *testing.T) {
	if os.Getenv("WB_COLLAB_PEER_ACTOR") != "1" {
		return
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		var request collaborationActorRequest
		if err := json.Unmarshal(reader.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		command := exec.CommandContext(ctx, request.Binary, request.Args...)
		output, err := command.CombinedOutput()
		cancel()
		result := collaborationActorResult{Output: string(output)}
		if err != nil {
			result.Error = err.Error()
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			t.Fatal(err)
		}
	}
	if err := reader.Err(); err != nil {
		t.Fatal(err)
	}
}

type collaborationActor struct {
	command *exec.Cmd
	input   *json.Encoder
	output  *json.Decoder
	stderr  *bytes.Buffer
	binary  string
}

func startCollaborationActor(t *testing.T, binary, root, id string) *collaborationActor {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestE2ECollaborationPeerActor$")
	command.Env = append(os.Environ(), "WB_COLLAB_PEER_ACTOR=1")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = command.Process.Kill()
		if err := command.Wait(); err != nil && !strings.Contains(err.Error(), "killed") {
			t.Errorf("actor %s exited: %v: %s", id, err, stderr)
		}
	})
	if _, err := session.Register(filepath.Join(root, ".wb", session.DirName), session.Record{
		PID: command.Process.Pid, WBSessionID: id, Runtime: "codex", NativeHarnessID: id,
	}); err != nil {
		t.Fatal(err)
	}
	if exact, live, err := session.LookupExact(filepath.Join(root, ".wb", session.DirName), command.Process.Pid); err != nil || !live || exact.WBSessionID != id {
		t.Fatalf("actor %s registration = %+v, live=%t, err=%v", id, exact, live, err)
	}
	return &collaborationActor{command: command, input: json.NewEncoder(stdin), output: json.NewDecoder(stdout), stderr: stderr, binary: binary}
}

func (actor *collaborationActor) run(t *testing.T, args ...string) collaborationActorResult {
	t.Helper()
	request := collaborationActorRequest{Binary: actor.binary, Args: args}
	if err := actor.input.Encode(request); err != nil {
		t.Fatalf("send actor request: %v: %s", err, actor.stderr)
	}
	var result collaborationActorResult
	if err := actor.output.Decode(&result); err != nil {
		t.Fatalf("read actor result: %v: %s", err, actor.stderr)
	}
	return result
}

func requireCollaborationActorSuccess(t *testing.T, phase string, result collaborationActorResult) string {
	t.Helper()
	if result.Error != "" {
		t.Fatalf("%s: %s: %s", phase, result.Error, result.Output)
	}
	return result.Output
}

func TestE2ECollaborationDistinctRegisteredProcessesExchangeAndTransfer(t *testing.T) {
	// initGCFixture uses t.Setenv and a real local Git remote; the two actor
	// processes below remain live until all CLI calls have completed.
	projects, home, worktree := initGCFixture(t)
	binary := buildWB(t)
	claimPaths, err := filepath.Glob(filepath.Join(home, "worklogs", "*", "runs", "*", "claims", "*.json"))
	if err != nil || len(claimPaths) == 0 {
		t.Fatalf("fixture claim paths = %v, %v", claimPaths, err)
	}
	claimsBefore := make(map[string][]byte, len(claimPaths))
	for _, path := range claimPaths {
		claimsBefore[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	headBefore, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	owner := startCollaborationActor(t, binary, projects, "wbs-native-owner")
	peer := startCollaborationActor(t, binary, projects, "wbs-native-peer")
	if owner.command.Process.Pid == peer.command.Process.Pid {
		t.Fatal("native actors shared a PID")
	}
	service, err := newCollaborationService(&invocation{projectsRoot: projects})
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := service.Ports.Resolve(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := service.Ports.ObserveLegacy(checkout)
	if err != nil || legacy.ID == "" {
		t.Fatalf("legacy owner = %+v, %v", legacy, err)
	}
	if output := requireCollaborationActorSuccess(t, "owner take", owner.run(t, "worktree", "take-ownership", checkout.ID,
		"--expected-owner", legacy.ID, "--force", "--reason", "native handoff")); !strings.Contains(output, "owner wbs-native-owner") {
		t.Fatalf("owner take output = %q", output)
	}
	if output := requireCollaborationActorSuccess(t, "peer join", peer.run(t, "worktree", "join", worktree)); !strings.Contains(output, "owner wbs-native-owner") {
		t.Fatalf("peer join output = %q", output)
	}
	messageFile := filepath.Join(t.TempDir(), "peer-message")
	if err := os.WriteFile(messageFile, []byte("native peer message"), 0o600); err != nil {
		t.Fatal(err)
	}
	sent := requireCollaborationActorSuccess(t, "owner send", owner.run(t, "worktree", "message", "send", worktree,
		"--to-session", "wbs-native-peer", "--idempotency-key", "native-message-one", "--message-file", messageFile))
	fields := strings.Fields(sent)
	if len(fields) < 2 || fields[0] != "message" {
		t.Fatalf("send receipt = %q", sent)
	}
	messageID := fields[1]
	if output := requireCollaborationActorSuccess(t, "durable retry", owner.run(t, "worktree", "message", "send", worktree,
		"--to-session", "wbs-native-peer", "--idempotency-key", "native-message-one", "--message-file", messageFile)); !strings.Contains(output, "replay=true") || !strings.Contains(output, messageID) {
		t.Fatalf("retry = %q", output)
	}
	inbox := requireCollaborationActorSuccess(t, "peer inbox", peer.run(t, "worktree", "message", "inbox", worktree))
	if !strings.Contains(inbox, messageID) || !strings.Contains(inbox, "native peer message") {
		t.Fatalf("peer inbox omitted recorded message: %q", inbox)
	}
	if output := requireCollaborationActorSuccess(t, "peer ack", peer.run(t, "worktree", "message", "ack", worktree, messageID)); !strings.Contains(output, "consumed through") {
		t.Fatalf("ack = %q", output)
	}
	if output := requireCollaborationActorSuccess(t, "empty peer inbox", peer.run(t, "worktree", "message", "inbox", worktree)); output != "" {
		t.Fatalf("acknowledged message remained visible: %q", output)
	}
	if output := requireCollaborationActorSuccess(t, "owner transfer", owner.run(t, "worktree", "transfer-ownership", worktree,
		"--to-session", "wbs-native-peer")); !strings.Contains(output, "owner wbs-native-peer") {
		t.Fatalf("transfer = %q", output)
	}
	stale := owner.run(t, "worktree", "take-ownership", worktree, "--expected-owner", "wbs-native-owner", "--force", "--reason", "stale")
	if stale.Error == "" || !strings.Contains(stale.Output, "wbs-native-peer") {
		t.Fatalf("stale expected-owner CAS accepted or hid current owner: %+v", stale)
	}
	if output := requireCollaborationActorSuccess(t, "former owner leave", owner.run(t, "worktree", "leave", worktree)); !strings.Contains(output, "left") {
		t.Fatalf("leave = %q", output)
	}
	view, err := service.Inspect(context.Background(), worktree)
	if err != nil || view.Owner != "wbs-native-peer" || len(view.Joined) != 1 || view.Joined[0].SessionID != "wbs-native-peer" {
		t.Fatalf("final view = %+v, %v", view, err)
	}
	headAfter, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD").Output()
	if err != nil || !bytes.Equal(headBefore, headAfter) {
		t.Fatalf("coordination changed checkout HEAD: before=%q after=%q err=%v", headBefore, headAfter, err)
	}
	claimPathsAfter, err := filepath.Glob(filepath.Join(home, "worklogs", "*", "runs", "*", "claims", "*.json"))
	if err != nil || !slices.Equal(claimPaths, claimPathsAfter) {
		t.Fatalf("coordination changed immutable claim set: before=%v after=%v err=%v", claimPaths, claimPathsAfter, err)
	}
	for path, before := range claimsBefore {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("coordination changed immutable claim %s: %v", path, err)
		}
	}
}
