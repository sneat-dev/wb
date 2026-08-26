package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestSessionParkResumeAcrossProcessTransport is the product boundary for a
// parked task: a real source CLI parks a managed, pushed worktree; its remote
// resume crosses the SSH command contract into a separately configured target
// CLI; and the target reconstructs the exact commit before releasing the
// intended successor session. The controlled ssh/tmux/codex executables are
// process-level transport/harness stand-ins, not injected Go dependencies.
func TestSessionParkResumeAcrossProcessTransport(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source-projects")
	targetRoot := filepath.Join(root, "target-projects")
	sourceHome := filepath.Join(root, "source-home")
	targetHome := filepath.Join(root, "target-home")
	fakeBin := filepath.Join(root, "bin")
	tmuxState := filepath.Join(root, "tmux")
	harnessReceipt := filepath.Join(root, "target-harness-receipt")
	for _, directory := range []string{sourceRoot, targetRoot, fakeBin, tmuxState} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(wbhome.EnvOverride, sourceHome)
	t.Setenv(wbhome.EnvMigrationCompat, "")

	binary := buildJourneyWB(t)
	writeJourneyExecutable(t, filepath.Join(fakeBin, "tmux"), journeyTmuxScript)
	writeJourneyExecutable(t, filepath.Join(fakeBin, "codex"), journeyCodexScript)
	writeJourneyExecutable(t, filepath.Join(fakeBin, "ssh"), journeySSHScript)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_TEST_TARGET_HOME", targetHome)
	t.Setenv("WB_TEST_TARGET_WB_HOME", targetHome)
	t.Setenv("WB_TEST_TARGET_PROJECTS_ROOT", targetRoot)
	t.Setenv("WB_TEST_TMUX_DIR", tmuxState)
	t.Setenv("WB_TEST_HARNESS_RECEIPT", harnessReceipt)
	t.Cleanup(func() { terminateJourneyTmuxProcesses(t, tmuxState) })

	sourceMembers := prepareJourneySourceWorktrees(t, root, sourceRoot)
	source := session.Record{
		PID: os.Getpid(), WBSessionID: "wbs-journey-source", Machine: "source",
		Runtime: "codex", Model: "gpt-5.6-luna", StartedAt: time.Now().UTC(),
	}
	sessionDir := filepath.Join(sourceHome, session.DirName)
	if _, err := session.Register(sessionDir, source); err != nil {
		t.Fatal(err)
	}
	for _, member := range sourceMembers {
		if err := worktrees.RecordCustody(member.Worktree, "", "journey source session", worktrees.AgentIdentity{
			Runtime: source.Runtime, AgentID: source.WBSessionID, Model: source.Model, PID: source.PID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	continuation := "finish the intentionally tiny remote park/resume task"
	continuationPath := filepath.Join(root, "continuation.md")
	if err := os.WriteFile(continuationPath, []byte(continuation+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	parkedRaw := runJourneyWB(t, binary, sourceRoot, "session", "park", "--context-file", continuationPath, "--format", "json")
	if bytes.Contains(parkedRaw, []byte(continuation)) {
		t.Fatalf("park output disclosed private continuation: %s", parkedRaw)
	}
	var parked struct {
		ParkedSessionID string                 `json:"parked_session_id"`
		Status          string                 `json:"status"`
		Worktrees       []sessionpark.Worktree `json:"worktrees"`
	}
	if err := json.Unmarshal(parkedRaw, &parked); err != nil {
		t.Fatal(err)
	}
	assertJourneyParkedBundle(t, parked.Worktrees, sourceMembers)
	assertJourneySourceCustody(t, sourceHome, parked.Worktrees, source)

	targetConfig := filepath.Join(targetHome, ".config", "wb", "wb.yaml")
	if err := os.MkdirAll(filepath.Dir(targetConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetConfig, []byte("remote:\n  repo: acme/app\n  machine: target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceConfig := filepath.Join(root, "source-wb.yaml")
	if err := os.WriteFile(sourceConfig, []byte(fmt.Sprintf("session_move:\n  targets:\n    target:\n      default_courier: ssh\n      ssh:\n        host: journey-target\n        wb_path: %s\n", binary)), 0o600); err != nil {
		t.Fatal(err)
	}

	resumedRaw := runJourneyWB(t, binary, sourceRoot, "session", "resume", parked.ParkedSessionID,
		"--to", "target", "--via", "ssh", "--config", sourceConfig, "--format", "json")
	if bytes.Contains(resumedRaw, []byte(continuation)) {
		t.Fatalf("resume output disclosed private continuation: %s", resumedRaw)
	}
	var resumed struct {
		Status    string               `json:"status"`
		Successor *session.Record      `json:"successor"`
		Receipt   *sessionpark.Receipt `json:"receipt"`
	}
	if err := json.Unmarshal(resumedRaw, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Status != string(sessionpark.StatusResumed) || resumed.Successor == nil || resumed.Receipt == nil ||
		resumed.Successor.WBSessionID != resumed.Receipt.SuccessorWBSessionID || resumed.Successor.PredecessorWBSessionID != source.WBSessionID ||
		len(resumed.Receipt.Members) != len(sourceMembers) {
		t.Fatalf("remote resume result = %#v, want one durable target receipt and its exact successor identity", resumed)
	}

	sourceStore := sessionpark.NewStore(filepath.Join(sourceHome, "parked-sessions"))
	state, err := sourceStore.Load(parked.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != sessionpark.StatusResumed || state.Successor == nil || state.Successor.WBSessionID != resumed.Receipt.SuccessorWBSessionID ||
		state.RemoteReceipt == nil || !reflect.DeepEqual(state.RemoteReceipt, resumed.Receipt) || len(state.Events) == 0 {
		t.Fatalf("source terminal custody state = %#v", state)
	}
	terminalEvent := state.Events[len(state.Events)-1]
	if terminalEvent.Type != "resumed" || terminalEvent.RemoteResumeID != resumed.Receipt.ResumeID || terminalEvent.TargetMachine != "target" || terminalEvent.RequestDigest != resumed.Receipt.RequestDigest || terminalEvent.Successor == nil || terminalEvent.Successor.WBSessionID != resumed.Receipt.SuccessorWBSessionID {
		t.Fatalf("source terminal custody event = %#v", terminalEvent)
	}
	assertJourneyTargetBundle(t, targetHome, resumed.Receipt, sourceMembers)
	targetSessions, err := session.List(filepath.Join(targetHome, session.DirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(targetSessions) != 1 || targetSessions[0].WBSessionID != resumed.Receipt.SuccessorWBSessionID || targetSessions[0].State != session.StateLive {
		t.Fatalf("target session registration = %#v", targetSessions)
	}
	targetEvents := make(map[string][]byte, len(resumed.Receipt.Members))
	for _, member := range resumed.Receipt.Members {
		targetEvents[member.TargetPath] = readJourneyTargetEvents(t, member.TargetPath)
	}
	resumedAgainRaw := runJourneyWB(t, binary, sourceRoot, "session", "resume", parked.ParkedSessionID,
		"--to", "target", "--via", "ssh", "--config", sourceConfig, "--format", "json")
	var resumedAgain struct {
		Status    string               `json:"status"`
		Successor *session.Record      `json:"successor"`
		Receipt   *sessionpark.Receipt `json:"receipt"`
	}
	if err := json.Unmarshal(resumedAgainRaw, &resumedAgain); err != nil {
		t.Fatal(err)
	}
	if resumedAgain.Status != string(sessionpark.StatusResumed) || !reflect.DeepEqual(resumedAgain.Receipt, resumed.Receipt) || !reflect.DeepEqual(resumedAgain.Successor, resumed.Successor) {
		t.Fatalf("remote resume replay = %#v, want the original receipt and successor %#v", resumedAgain, resumed)
	}
	stateAgain, err := sourceStore.Load(parked.ParkedSessionID)
	if err != nil || !reflect.DeepEqual(stateAgain, state) {
		t.Fatalf("source replay custody state=%#v err=%v, want unchanged terminal source state %#v", stateAgain, err, state)
	}
	targetSessionsAgain, err := session.List(filepath.Join(targetHome, session.DirName))
	if err != nil || !reflect.DeepEqual(targetSessionsAgain, targetSessions) {
		t.Fatalf("target replay session registrations=%#v err=%v, want no second successor %#v", targetSessionsAgain, err, targetSessions)
	}
	for _, member := range resumed.Receipt.Members {
		if got := readJourneyTargetEvents(t, member.TargetPath); !bytes.Equal(got, targetEvents[member.TargetPath]) {
			t.Fatalf("target replay mutated member custody events for %s", member.Repository)
		}
	}
	harnessRaw := readJourneyHarnessReceipt(t, harnessReceipt)
	if !bytes.Contains(harnessRaw, []byte("WB_SESSION_CONTINUATION_FILE=")) || !bytes.Contains(harnessRaw, []byte(continuation)) ||
		!bytes.Contains(harnessRaw, []byte("as session "+resumed.Receipt.SuccessorWBSessionID)) {
		t.Fatalf("target harness receipt = %q, want the resumed identity and private continuation file", harnessRaw)
	}
	for _, member := range resumed.Receipt.Members {
		if !bytes.Contains(harnessRaw, []byte(member.TargetPath)) {
			t.Fatalf("target harness context does not include target member %s: %q", member.TargetPath, harnessRaw)
		}
	}
	continuationFile := journeyHarnessContinuationPath(t, harnessRaw)
	info, err := os.Stat(continuationFile)
	if err != nil {
		t.Fatalf("stat private successor continuation: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("private successor continuation permissions = %s, want regular 0600", info.Mode())
	}
}

func buildJourneyWB(t *testing.T) string {
	t.Helper()
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	binary := filepath.Join(t.TempDir(), "wb")
	command := exec.Command("go", "build", "-o", binary, "./cmd/wb")
	command.Dir = repoRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build journey wb: %v\n%s", err, output)
	}
	return binary
}

type journeySourceMember struct {
	Repository string
	Remote     string
	Worktree   string
	Commit     string
}

func prepareJourneySourceWorktrees(t *testing.T, root, projectsRoot string) []journeySourceMember {
	t.Helper()
	repositories := []string{"acme/alpha", "acme/beta"}
	for _, repository := range repositories {
		remote := filepath.Join(root, "remotes", filepath.FromSlash(repository)+".git")
		if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
			t.Fatal(err)
		}
		journeyGit(t, root, "init", "--bare", "--initial-branch=main", remote)
		canonical := filepath.Join(projectsRoot, filepath.FromSlash(repository))
		if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
			t.Fatal(err)
		}
		journeyGit(t, root, "clone", remote, canonical)
		journeyGit(t, canonical, "config", "user.name", "WB Journey")
		journeyGit(t, canonical, "config", "user.email", "wb-journey@example.test")
		if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("park journey for "+repository+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		journeyGit(t, canonical, "add", "README.md")
		journeyGit(t, canonical, "-c", "commit.gpgSign=false", "commit", "-m", "initial")
		journeyGit(t, canonical, "push", "origin", "main")
	}
	prompt := filepath.Join(root, "source-prompt.md")
	if err := os.WriteFile(prompt, []byte("park/resume product journey\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := worktrees.Create(context.Background(), repositories, worktrees.CreateOptions{
		ProjectsRoot: projectsRoot, Operation: "park-journey", Branch: "feature/park-journey", BranchChosen: true, Base: "main",
		WorkLog: worktrees.WorkLogOptions{EffortID: "park-journey", RunID: "park-journey-run", Initiator: "journey-test",
			AgentID: "wbs-journey-source", AgentRuntime: "codex", Model: "gpt-5.6-luna", OriginalPrompt: prompt, RequireOriginalPrompt: true},
	})
	if err != nil || len(created) != len(repositories) {
		t.Fatalf("create source managed worktrees=%#v err=%v", created, err)
	}
	members := make([]journeySourceMember, 0, len(created))
	for _, result := range created {
		journeyGit(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)
		members = append(members, journeySourceMember{
			Repository: result.Repository,
			Remote:     filepath.Join(root, "remotes", filepath.FromSlash(result.Repository)+".git"),
			Worktree:   result.WorktreeDir,
			Commit:     journeyGitOutput(t, result.WorktreeDir, "rev-parse", "HEAD"),
		})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Repository < members[j].Repository })
	return members
}

func assertJourneyParkedBundle(t *testing.T, parked []sessionpark.Worktree, source []journeySourceMember) {
	t.Helper()
	if len(parked) != len(source) {
		t.Fatalf("parked worktrees = %#v, want %d exact remotely reconstructable members", parked, len(source))
	}
	byRepository := make(map[string]journeySourceMember, len(source))
	for _, member := range source {
		byRepository[member.Repository] = member
	}
	for _, member := range parked {
		want, found := byRepository[member.Repository]
		if !found || member.Head != want.Commit || member.RemoteHead != want.Commit || member.RepositoryRemote != want.Remote || member.WorkLogReference == "" {
			t.Fatalf("parked member = %#v, want exact member in %#v", member, source)
		}
		delete(byRepository, member.Repository)
	}
	if len(byRepository) != 0 {
		t.Fatalf("park omitted source members: %#v", byRepository)
	}
}

func assertJourneyTargetBundle(t *testing.T, targetHome string, receipt *sessionpark.Receipt, source []journeySourceMember) {
	t.Helper()
	if len(receipt.Members) != len(source) {
		t.Fatalf("receipt members = %#v, want %d", receipt.Members, len(source))
	}
	byRepository := make(map[string]journeySourceMember, len(source))
	for _, member := range source {
		byRepository[member.Repository] = member
	}
	for index, member := range receipt.Members {
		want, found := byRepository[member.Repository]
		if !found || !strings.HasPrefix(member.MemberID, fmt.Sprintf("m-%03d-", index+1)) || member.Commit != want.Commit || member.Pin != sessionpark.MemberPin(receipt.ResumeID, member.MemberID) {
			t.Fatalf("receipt member %d = %#v, want ordered exact source member in %#v", index, member, source)
		}
		if got := journeyGitOutput(t, member.TargetPath, "rev-parse", "HEAD"); got != want.Commit {
			t.Fatalf("target %s commit = %s, want deployed source revision %s", member.Repository, got, want.Commit)
		}
		if got := journeyGitOutput(t, member.TargetPath, "branch", "--show-current"); got != member.Pin {
			t.Fatalf("target %s branch = %s, want exact receipt pin %s", member.Repository, got, member.Pin)
		}
		assertJourneyTargetCustody(t, targetHome, member, receipt.SuccessorWBSessionID)
		delete(byRepository, member.Repository)
	}
	if len(byRepository) != 0 {
		t.Fatalf("target receipt omitted source members: %#v", byRepository)
	}
}

func assertJourneySourceCustody(t *testing.T, sourceHome string, parked []sessionpark.Worktree, source session.Record) {
	t.Helper()
	if len(parked) != 2 {
		t.Fatalf("source custody requires two parked members, got %#v", parked)
	}
	for _, member := range parked {
		parts := strings.Split(strings.TrimPrefix(member.WorkLogReference, "worklog:"), "/")
		if len(parts) != 3 || member.OwnerEventID == "" {
			t.Fatalf("source member lacks exact Work Log/owner evidence: %#v", member)
		}
		claimPath := filepath.Join(sourceHome, "worklogs", parts[0], "runs", parts[1], "claims", parts[2]+".json")
		claimRaw, err := os.ReadFile(claimPath)
		if err != nil {
			t.Fatalf("read source claim %s: %v", claimPath, err)
		}
		var claim struct {
			Lifecycle    string `json:"lifecycle"`
			AgentID      string `json:"agent_id"`
			AgentRuntime string `json:"agent_runtime"`
		}
		if err := json.Unmarshal(claimRaw, &claim); err != nil {
			t.Fatal(err)
		}
		if claim.Lifecycle != "active" || claim.AgentID != source.WBSessionID || claim.AgentRuntime != source.Runtime {
			t.Fatalf("source claim %s = %#v, want active exact source owner", claimPath, claim)
		}
		events := readJourneyTargetEvents(t, member.WorktreeDir)
		if !bytes.Contains(events, []byte(`"id":"`+member.OwnerEventID+`"`)) || !bytes.Contains(events, []byte(`"agent":"codex/`+source.WBSessionID+`"`)) {
			t.Fatalf("source local Work Log misses parked owner evidence for %s: %s", member.Repository, events)
		}
	}
}

func assertJourneyTargetCustody(t *testing.T, targetHome string, member sessionpark.ReceiptMember, successorID string) {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(member.TargetWorkLogReference, "worklog:"), "/")
	if len(parts) != 3 || strings.Join(parts, "/") == member.TargetWorkLogReference {
		t.Fatalf("target Work Log reference %q is malformed", member.TargetWorkLogReference)
	}
	claimPath := filepath.Join(targetHome, "worklogs", parts[0], "runs", parts[1], "claims", parts[2]+".json")
	claimRaw, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatalf("read target claim %s: %v", claimPath, err)
	}
	var claim struct {
		Lifecycle       string `json:"lifecycle"`
		AgentID         string `json:"agent_id"`
		AgentRuntime    string `json:"agent_runtime"`
		ExternalHandoff *struct {
			TargetWorkLogReference string `json:"target_work_log_reference"`
		} `json:"external_handoff"`
	}
	if err := json.Unmarshal(claimRaw, &claim); err != nil {
		t.Fatal(err)
	}
	if claim.Lifecycle != "active" || claim.AgentID != successorID || claim.AgentRuntime != "codex" || claim.ExternalHandoff == nil || claim.ExternalHandoff.TargetWorkLogReference != member.TargetWorkLogReference {
		t.Fatalf("target claim %s = %#v, want active exact successor custody", claimPath, claim)
	}
	eventsRaw, err := os.ReadFile(filepath.Join(member.TargetPath, ".wb", "local", "worklog", "events.jsonl"))
	if err != nil {
		t.Fatalf("read target local events for %s: %v", member.Repository, err)
	}
	var owner, completed bool
	for _, line := range bytes.Split(eventsRaw, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event struct {
			Type   string `json:"type"`
			Result string `json:"result"`
			Owner  *struct {
				Agent string `json:"agent"`
			} `json:"owner"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "owner_attached" && event.Owner != nil && event.Owner.Agent == "codex/"+successorID {
			owner = true
		}
		if event.Type == "handoff" && event.Result == "completed" {
			completed = true
		}
	}
	if !owner || !completed {
		t.Fatalf("target local custody for %s missing owner=%t completed=%t", member.Repository, owner, completed)
	}
	if _, err := os.Stat(filepath.Join(member.TargetPath, ".wb-worklog", "recovery.json")); err != nil {
		t.Fatalf("target Work Log projection for %s: %v", member.Repository, err)
	}
}

func journeyHarnessContinuationPath(t *testing.T, raw []byte) string {
	t.Helper()
	for _, line := range strings.Split(string(raw), "\n") {
		if value, found := strings.CutPrefix(line, "WB_SESSION_CONTINUATION_FILE="); found && value != "" {
			return value
		}
	}
	t.Fatalf("target harness did not record private continuation path: %q", raw)
	return ""
}

func readJourneyTargetEvents(t *testing.T, worktree string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(worktree, ".wb", "local", "worklog", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func runJourneyWB(t *testing.T, binary, projectsRoot string, args ...string) []byte {
	t.Helper()
	commandArgs := append([]string{"--projects-root", projectsRoot, "--non-interactive"}, args...)
	command := exec.Command(binary, commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("wb %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

func journeyGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func journeyGitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}

func writeJourneyExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func terminateJourneyTmuxProcesses(t *testing.T, tmuxState string) {
	t.Helper()
	entries, err := os.ReadDir(tmuxState)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".pid") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(tmuxState, entry.Name()))
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if readErr == nil && parseErr == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	}
}

func readJourneyHarnessReceipt(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			return raw
		}
		if !os.IsNotExist(err) || time.Now().After(deadline) {
			t.Fatalf("read target harness receipt: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

const journeySSHScript = `#!/bin/sh
set -eu
while [ "$1" != "--" ]; do shift; done
shift
target="$1"
shift
remote_wb="$1"
shift
exec env HOME="$WB_TEST_TARGET_HOME" WB_HOME="$WB_TEST_TARGET_WB_HOME" "$remote_wb" --projects-root "$WB_TEST_TARGET_PROJECTS_ROOT" "$@"
`

const journeyTmuxScript = `#!/bin/sh
set -eu
case "$1" in
new-session)
  shift
  [ "$1" = "-d" ] && shift
  [ "$1" = "-s" ]
  name="$2"
  shift 2
  [ "$1" = "-c" ]
  cwd="$2"
  shift 2
  (
    cd "$cwd"
    exec "$@"
  ) >/dev/null 2>"$WB_TEST_TMUX_DIR/$name.stderr" &
  echo "$!" >"$WB_TEST_TMUX_DIR/$name.pid"
  ;;
list-panes)
  name=""
  for value in "$@"; do
    case "$value" in =*) name="${value#=}" ;; esac
  done
  pid_file="$WB_TEST_TMUX_DIR/$name.pid"
  if [ -n "$name" ] && [ -f "$pid_file" ]; then
    pid="$(cat "$pid_file")"
    if kill -0 "$pid" 2>/dev/null; then
      echo "$pid"
      exit 0
    fi
  fi
  echo "can't find session: $name" >&2
  exit 1
  ;;
*)
  echo "unsupported fake tmux command: $*" >&2
  exit 2
  ;;
esac
`

const journeyCodexScript = `#!/bin/sh
set -eu
{
  echo "PID=$$"
  echo "WB_SESSION_CONTINUATION_FILE=${WB_SESSION_CONTINUATION_FILE:-}"
  printf 'ARGS='
  printf '%s ' "$@"
  printf '\n'
  if [ -n "${WB_SESSION_CONTINUATION_FILE:-}" ]; then
    cat "$WB_SESSION_CONTINUATION_FILE"
  fi
} >"$WB_TEST_HARNESS_RECEIPT"
trap 'exit 0' TERM INT
while :; do sleep 1; done
`
