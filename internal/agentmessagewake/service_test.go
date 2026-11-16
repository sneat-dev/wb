package agentmessagewake

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sneat-dev/wb/internal/agentmessage"
	"github.com/sneat-dev/wb/internal/filewrite"
)

type fakeConnection struct {
	calls    []string
	fail     string
	raw      json.RawMessage
	notified bool
	closed   bool
	params   any
}

func (f *fakeConnection) Call(method string, params any) (json.RawMessage, error) {
	f.calls = append(f.calls, method)
	if f.fail == method {
		return nil, errors.New("private server text")
	}
	switch method {
	case "thread/read":
		if f.raw != nil {
			return f.raw, nil
		}
		return json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}}}`), nil
	case "turn/start":
		f.params = params
		if f.raw != nil {
			return f.raw, nil
		}
		return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
	default:
		return json.RawMessage(`{}`), nil
	}
}
func (f *fakeConnection) Notify(_ string) error {
	f.notified = true
	if f.fail == "notification" {
		return errors.New("secret")
	}
	return nil
}
func (f *fakeConnection) Close() { f.closed = true }
func testService(t *testing.T) (Service, *fakeConnection) {
	t.Helper()
	c := &fakeConnection{}
	s := Service{Root: filepath.Join(t.TempDir(), "wake"), dial: func(context.Context, string) (connection, error) { return c, nil }}
	return s, c
}
func testMessage() agentmessage.Message {
	return agentmessage.Message{Version: 1, ID: strings.Repeat("a", 32), From: "vm", To: "mac", Body: "private message"}
}
func bindFixture(t *testing.T, s Service) {
	t.Helper()
	if err := s.Bind(context.Background(), "mac", "/tmp/codex.sock", "thread-1"); err != nil {
		t.Fatal(err)
	}
}

func TestBindingIsExplicitAndCannotSilentlyRetarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, c := testService(t)
	if state, err := s.Notify(ctx, testMessage()); err != nil || state != NotBound {
		t.Fatalf("%s %v", state, err)
	}
	bindFixture(t, s)
	bindFixture(t, s)
	if err := s.Bind(ctx, "mac", "/tmp/another.sock", "thread-1"); err == nil {
		t.Fatal("retarget accepted")
	}
	if !c.notified || !c.closed {
		t.Fatal("preflight lifecycle")
	}
	if err := s.Unbind("mac"); err != nil {
		t.Fatal(err)
	}
	if err := s.Unbind("mac"); err != nil {
		t.Fatal(err)
	}
	if state, err := s.Notify(ctx, testMessage()); err != nil || state != NotBound {
		t.Fatalf("%s %v", state, err)
	}
}

func TestAcceptedWakeIsDurablyFencedAndContainsOnlyInboxPointer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, c := testService(t)
	bindFixture(t, s)
	state, err := s.Notify(ctx, testMessage())
	if err != nil || state != Submitted {
		t.Fatalf("%s %v", state, err)
	}
	raw, _ := json.Marshal(c.params)
	if strings.Contains(string(raw), "private message") || !strings.Contains(string(raw), "wb message receive --agent mac") {
		t.Fatalf("unexpected prompt: %s", raw)
	}
	if !strings.Contains(string(raw), "wb message ack --agent mac "+testMessage().ID) || strings.Contains(string(raw), "--id") {
		t.Fatalf("incorrect acknowledgement command: %s", raw)
	}
	params := c.params.(map[string]any)
	if len(params) != 3 || params["threadId"] != "thread-1" {
		t.Fatalf("extra model or policy params: %#v", params)
	}
	before := len(c.calls)
	if state, err = s.Notify(ctx, testMessage()); err != nil || state != Submitted || len(c.calls) != before {
		t.Fatalf("replayed wake: %s %v", state, err)
	}
	if err = s.Unbind("mac"); err != nil {
		t.Fatal(err)
	}
	bindFixture(t, s)
	if state, err = s.Notify(ctx, testMessage()); err != nil || state != Submitted {
		t.Fatal(state, err)
	}
	data, err := os.ReadFile(filepath.Join(s.Root, "mac", testMessage().ID+".result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private message") {
		t.Fatal("body persisted to wake metadata")
	}
	info, err := os.Stat(filepath.Join(s.Root, "mac", "binding.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("binding permissions")
	}
	changed := testMessage()
	changed.Body = "changed"
	if state, err = s.Notify(ctx, changed); state != Ambiguous || err == nil {
		t.Fatal("mismatched message accepted")
	}
}

func TestUncertainSubmissionNeverAutomaticallyRetries(t *testing.T) {
	t.Parallel()
	s, c := testService(t)
	bindFixture(t, s)
	c.fail = "turn/start"
	state, err := s.Notify(context.Background(), testMessage())
	if state != Ambiguous || err == nil || strings.Contains(err.Error(), "private server text") {
		t.Fatal(state, err)
	}
	before := len(c.calls)
	c.fail = ""
	if state, err = s.Notify(context.Background(), testMessage()); state != Ambiguous || err != nil || len(c.calls) != before {
		t.Fatal("uncertain submission was retried")
	}
}

func TestPreflightErrorsAreSafeToRetryAndRedacted(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"dial", "initialize", "notification", "thread/read", "thread-mismatch", "thread-malformed"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			s, c := testService(t)
			bindFixture(t, s)
			c.fail = phase
			if phase == "dial" {
				s.dial = func(context.Context, string) (connection, error) { return nil, errors.New("private transport payload") }
			}
			if phase == "thread-mismatch" {
				c.raw = json.RawMessage(`{"thread":{"id":"other"}}`)
			}
			if phase == "thread-malformed" {
				c.raw = json.RawMessage(`bad`)
			}
			state, err := s.Notify(context.Background(), testMessage())
			if state != PreflightFailed || err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal(state, err)
			}
			c.fail = ""
			c.raw = nil
			s.dial = func(context.Context, string) (connection, error) { return c, nil }
			state, err = s.Notify(context.Background(), testMessage())
			if state != Submitted || err != nil {
				t.Fatal(state, err)
			}
		})
	}
}

func TestInvalidBindingAndInboxPathsAreRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := testService(t)
	for _, b := range []binding{{"relative", "thread-1"}, {"/socket", ""}, {"/socket", "../thread"}, {strings.Repeat("/", 4097), "thread"}} {
		if err := s.Bind(ctx, "mac", b.SocketPath, b.ThreadID); err == nil {
			t.Fatal("invalid binding")
		}
	}
	if err := s.Bind(ctx, "../alias", "/socket", "thread-1"); err == nil {
		t.Fatal("unsafe agent")
	}
	if err := s.Unbind("../alias"); err == nil {
		t.Fatal("unsafe agent")
	}
	msg := testMessage()
	msg.Version = 0
	if state, err := s.Notify(ctx, msg); state != PreflightFailed || err == nil {
		t.Fatal(state, err)
	}
	s.Root = "relative"
	if err := s.Bind(ctx, "mac", "/socket", "thread-1"); err == nil {
		t.Fatal("relative root")
	}
	if state, err := s.Notify(ctx, testMessage()); state != PreflightFailed || err == nil {
		t.Fatal(state, err)
	}
	if err := s.Unbind("mac"); err == nil {
		t.Fatal("bad root")
	}
	s.Root = filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(t.TempDir(), s.Root); err != nil {
		t.Fatal(err)
	}
	if err := s.Bind(ctx, "mac", "/socket", "thread-1"); err == nil {
		t.Fatal("symlink root")
	}
}

func TestCorruptRecordsCannotSubmit(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"binding.json", testMessage().ID + ".intent.json", testMessage().ID + ".result.json"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _ := testService(t)
			bindFixture(t, s)
			if err := os.WriteFile(filepath.Join(s.Root, "mac", name), []byte(`bad`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Notify(context.Background(), testMessage()); err == nil {
				t.Fatal("corrupt record accepted")
			}
		})
	}
	s, _ := testService(t)
	bindFixture(t, s)
	if err := os.WriteFile(filepath.Join(s.Root, "mac", "binding.json"), []byte(`{"socket_path":"relative","thread_id":"thread-1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if state, err := s.Notify(context.Background(), testMessage()); state != PreflightFailed || err == nil {
		t.Fatal(state, err)
	}
	if err := os.Remove(filepath.Join(s.Root, "mac", "binding.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.Root, "mac", "binding.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Unbind("mac"); err == nil {
		t.Fatal("directory binding unlinked")
	}
}

func TestPersistenceFailuresNeverRepeatPossibleSubmissions(t *testing.T) {
	t.Parallel()
	for _, failResult := range []bool{false, true} {
		t.Run(map[bool]string{false: "intent", true: "receipt"}[failResult], func(t *testing.T) {
			t.Parallel()
			s, c := testService(t)
			bindFixture(t, s)
			s.write = func(dir *os.File, name string, value any, idempotent bool) error {
				if strings.Contains(name, "result") == failResult {
					return errors.New("disk failure")
				}
				return filewrite.WriteJSONImmutableAt(dir, name, value, idempotent, nil)
			}
			expected := PreflightFailed
			if failResult {
				expected = Ambiguous
			}
			state, err := s.Notify(context.Background(), testMessage())
			if state != expected || err == nil {
				t.Fatal(state, err)
			}
			count := 0
			for _, call := range c.calls {
				if call == "turn/start" {
					count++
				}
			}
			if failResult && count != 1 || !failResult && count != 0 {
				t.Fatal("wrong submit count", count)
			}
		})
	}
}

func TestConcurrentIdenticalMessagesSubmitAtMostOnce(t *testing.T) {
	t.Parallel()
	s, _ := testService(t)
	bindFixture(t, s)
	var mu sync.Mutex
	submitted := 0
	s.dial = func(context.Context, string) (connection, error) {
		return &countConnection{fakeConnection: fakeConnection{}, count: func() { mu.Lock(); submitted++; mu.Unlock() }}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.Notify(context.Background(), testMessage()) }()
	}
	wg.Wait()
	if submitted != 1 {
		t.Fatal("submission count", submitted)
	}
}

type countConnection struct {
	fakeConnection
	count func()
}

func (c *countConnection) Call(method string, params any) (json.RawMessage, error) {
	if method == "turn/start" {
		c.count()
	}
	return c.fakeConnection.Call(method, params)
}

func TestUnloadedAndErroredThreadsAreNeverResumed(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"notLoaded", "systemError", ""} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			s, c := testService(t)
			c.raw = json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"` + status + `"}}}`)
			if err := s.Bind(context.Background(), "mac", "/socket", "thread-1"); err == nil {
				t.Fatal("unloaded target bound")
			}
			for _, call := range c.calls {
				if call == "turn/start" || call == "thread/resume" {
					t.Fatal("unexpected mutation")
				}
			}
		})
	}
}

func TestActiveThreadCanBeBoundAndDirectoryHardeningFailureStopsBind(t *testing.T) {
	t.Parallel()
	s, c := testService(t)
	c.raw = json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"active"}}}`)
	bindFixture(t, s)
	s.chmod = func(*os.File, os.FileMode) error { return errors.New("chmod refused") }
	if err := s.Bind(context.Background(), "mac", "/socket", "thread-1"); err == nil {
		t.Fatal("hardening failure ignored")
	}
}

func TestMalformedTurnReceiptRemainsAmbiguous(t *testing.T) {
	t.Parallel()
	s, c := testService(t)
	bindFixture(t, s)
	c.raw = json.RawMessage(`{"thread":{"id":"thread-1","status":{"type":"idle"}}}`)
	if state, err := s.Notify(context.Background(), testMessage()); state != Ambiguous || err == nil {
		t.Fatal(state, err)
	}
}

func TestBindFirstCreatesPrivateMessageAncestorsUsableByInbox(t *testing.T) {
	t.Parallel()
	projects := filepath.Join(t.TempDir(), "projects")
	if err := os.Mkdir(projects, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(projects, 0755); err != nil {
		t.Fatal(err)
	}
	messageRoot := filepath.Join(projects, ".wb", "messages")
	s, _ := testService(t)
	s.Root = filepath.Join(messageRoot, "wake")
	s.ProjectsRoot = projects
	bindFixture(t, s)
	for _, path := range []string{filepath.Join(projects, ".wb"), messageRoot, s.Root, filepath.Join(s.Root, "mac")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0700 {
			t.Errorf("new message directory %s mode = %04o, want 0700", path, got)
		}
	}
	info, err := os.Stat(projects)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0755 {
		t.Errorf("existing projects mode changed to %04o", got)
	}
	inbox := agentmessage.Store{Root: messageRoot, Notify: s.Notify}
	receipt, err := inbox.Deliver(context.Background(), testMessage())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "durable" || receipt.Wake != Submitted {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	messages, err := inbox.Receive(context.Background(), "mac", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0] != testMessage() {
		t.Fatalf("unexpected inbox: %+v", messages)
	}
}

func TestMissingIntentAfterFailedWriteAllowsSafeRetry(t *testing.T) {
	t.Parallel()
	s, c := testService(t)
	bindFixture(t, s)
	s.write = func(*os.File, string, any, bool) error { return errors.New("write refused before publication") }
	if status, err := s.Notify(context.Background(), testMessage()); status != PreflightFailed || err == nil {
		t.Fatal(status, err)
	}
	for _, call := range c.calls {
		if call == "turn/start" {
			t.Fatal("input submitted without a recorded intent")
		}
	}
	s.write = nil
	if status, err := s.Notify(context.Background(), testMessage()); status != Submitted || err != nil {
		t.Fatal(status, err)
	}
	count := 0
	for _, call := range c.calls {
		if call == "turn/start" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("wrong submission count", count)
	}
}

func TestFailedIntentWriteWithCompetingOrUnreadableEntryNeverSubmits(t *testing.T) {
	t.Parallel()
	for _, unreadable := range []bool{false, true} {
		name := "competing-intent"
		if unreadable {
			name = "unreadable-intent"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, c := testService(t)
			bindFixture(t, s)
			s.write = func(dir *os.File, name string, value any, _ bool) error {
				if unreadable {
					if err := os.Symlink("missing", filepath.Join(s.Root, "mac", name)); err != nil {
						return err
					}
				} else if err := filewrite.WriteJSONImmutableAt(dir, name, value, false, nil); err != nil {
					return err
				}
				return errors.New("publication outcome uncertain")
			}
			if status, err := s.Notify(context.Background(), testMessage()); status != Ambiguous || err == nil {
				t.Fatal(status, err)
			}
			s.write = nil
			if status, _ := s.Notify(context.Background(), testMessage()); status != Ambiguous {
				t.Fatal(status)
			}
			for _, call := range c.calls {
				if call == "turn/start" {
					t.Fatal("competing or unreadable intent caused submission")
				}
			}
		})
	}
}

func TestBindingReplayMustSyncDirectoryBeforeReportingSuccess(t *testing.T) {
	t.Parallel()
	s, _ := testService(t)
	bindFixture(t, s)
	synced := 0
	s.syncDirectory = func(*os.File) error { synced++; return errors.New("sync refused") }
	if err := s.Bind(context.Background(), "mac", "/tmp/codex.sock", "thread-1"); err == nil {
		t.Fatal("unsynced binding replay succeeded")
	}
	if synced != 1 {
		t.Fatal("binding replay did not sync")
	}
	s.syncDirectory = nil
	bindFixture(t, s)
}

func TestWakeFenceSyncFailuresRemainAmbiguousWithoutAdditionalSubmission(t *testing.T) {
	t.Parallel()
	for _, failResult := range []bool{false, true} {
		name := "intent-sync"
		if failResult {
			name = "result-sync"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, c := testService(t)
			bindFixture(t, s)
			syncs := 0
			s.syncDirectory = func(dir *os.File) error {
				syncs++
				if !failResult || syncs >= 2 {
					return errors.New("sync failed")
				}
				return dir.Sync()
			}
			if status, err := s.Notify(context.Background(), testMessage()); status != Ambiguous || err == nil {
				t.Fatal(status, err)
			}
			before := len(c.calls)
			if status, err := s.Notify(context.Background(), testMessage()); status != Ambiguous || err == nil {
				t.Fatal("unsynced replay", status, err)
			}
			if len(c.calls) != before {
				t.Fatal("replay contacted Codex")
			}
			s.syncDirectory = nil
			expected := Ambiguous
			if failResult {
				expected = Submitted
			}
			if status, err := s.Notify(context.Background(), testMessage()); status != expected || err != nil {
				t.Fatal("synced replay", status, err)
			}
			count := 0
			for _, call := range c.calls {
				if call == "turn/start" {
					count++
				}
			}
			expectedCount := 0
			if failResult {
				expectedCount = 1
			}
			if count != expectedCount {
				t.Fatal("wrong submission count", count)
			}
		})
	}
}

func TestPrivateDirectoryCreationAndReplaySyncTheirParent(t *testing.T) {
	t.Parallel()
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	syncs := 0
	opener := privateOpener{sync: func(int) error { syncs++; return nil }}
	if err = opener.Mkdir(int(dir.Fd()), "new"); err != nil {
		t.Fatal(err)
	}
	if err = opener.Mkdir(int(dir.Fd()), "new"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("replay must preserve EEXIST: %v", err)
	}
	if syncs != 2 {
		t.Fatal("parent was not synced on creation and replay", syncs)
	}
	opener.sync = func(int) error { return errors.New("parent sync failed") }
	if err = opener.Mkdir(int(dir.Fd()), "new"); err == nil || errors.Is(err, os.ErrExist) {
		t.Fatal("parent sync failure not surfaced", err)
	}
	if err = opener.Mkdir(-1, "new"); err == nil {
		t.Fatal("invalid directory descriptor accepted")
	}
}

func TestChildPublicationSyncFailureStopsBeforeCodex(t *testing.T) {
	t.Parallel()
	s, c := testService(t)
	s.syncParent = func(*os.File) error { return errors.New("parent sync failed") }
	if err := s.Bind(context.Background(), "mac", "/tmp/codex.sock", "thread-1"); err == nil {
		t.Fatal("binding accepted before child directory publication durable")
	}
	if len(c.calls) != 0 {
		t.Fatal("contacted Codex with undurable directory")
	}
	s.syncParent = nil
	bindFixture(t, s)
}

func TestInvalidAgentDirectoryCannotAcquireWakeState(t *testing.T) {
	t.Parallel()
	s, _ := testService(t)
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(s.Root, "mac")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.directory("mac"); err == nil {
		t.Fatal("symlinked agent directory accepted")
	}
}
