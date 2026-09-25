//go:build unix

package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// triggerContext is a context.Context whose Err() reports
// context.DeadlineExceeded from the moment trigger() is called, and whose
// Done() channel closes at exactly the same moment. context.WithDeadline
// propagates a foreign parent's Err() into the child it derives (see the
// standard library's propagateCancel: a parent that is not itself a
// *cancelCtx is watched by a goroutine that copies parent.Err() into the
// child once parent.Done() fires), so wrapping this as the base context
// passed to WaitForCommitChecks makes sliceCtx.Err() == DeadlineExceeded an
// event this test controls directly, rather than a real clock it races.
type triggerContext struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newTriggerContext(parent context.Context) *triggerContext {
	return &triggerContext{Context: parent, done: make(chan struct{})}
}

func (c *triggerContext) Done() <-chan struct{} { return c.done }

func (c *triggerContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (c *triggerContext) trigger() { c.once.Do(func() { close(c.done) }) }

// TestWaitForCommitChecksPreservesPriorReasonWhenAuthorityFailsAtDeadline
// drives waitForCommitChecks' other required-check-authority branch (#766):
// when the slice's own deadline is what kills the required-check authority
// read (rather than the read failing quickly on its own), the function must
// return the prior observation's reason unchanged rather than overwrite it
// with a fresh "authority is unavailable" reason it never got a complete
// receipt for.
//
// Round-3 review (#766) proved the first version of this test raced a real
// 2s slice deadline: at 130-160ms of added latency per fake `gh` call it
// passed 0/2 times on line 224, taking one of three other
// identically-asserting pending-with-preserved-reason paths instead
// (ciwait.go:406, or the deadline-exceeded early returns in
// pullRequestIdentity/targetHead at ciwait.go:131-134/144-147). Passing
// while missing the very branch under test is exactly the flake class #766
// exists to describe.
//
// This version removes the race entirely by making "the deadline expires"
// an event the test raises, not a duration it waits out. The fake `gh`
// counts its own `/pulls/17` calls; every observation makes exactly three
// (pullRequestIdentity, commitChecks' own head-match read, then
// requiredChecksReceipt's pullRequestTargetsBase -- the exact call this
// test targets). On the sixth call overall -- the second observation's
// authority read -- it signals a FIFO this test's goroutine is already
// blocked reading, then hangs (via `exec sleep 30`, replacing its own
// process image so context cancellation actually kills it rather than
// leaving an orphaned grandchild holding the output pipe open). Only once
// that signal is received does the test call triggerCtx.trigger(), which
// is what makes sliceCtx.Err() become DeadlineExceeded and simultaneously
// kills the hanging fake `gh` process via the exec.CommandContext
// cancellation the killed subprocess's own context already carries. The
// slice itself is set to 5 minutes so the real wall clock is never in a
// position to decide anything. A final assertion that the counter file
// reads exactly "6" fails loudly if some other path was taken instead.
func TestWaitForCommitChecksPreservesPriorReasonWhenAuthorityFailsAtDeadline(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(t.TempDir(), "pulls-calls")
	fifo := filepath.Join(t.TempDir(), "trigger.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	head := "dddddddddddddddddddddddddddddddddddddddd"
	target := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then echo '{"object":{"sha":"` + target + `"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/compare/` + target + `...` + head + `'; then echo '{"status":"identical","base_commit":{"sha":"` + target + `"},"merge_base_commit":{"sha":"` + target + `"}}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then sleep "${WB_TEST_CALL_DELAY:-0}"; echo '{"total_count":0,"check_runs":[]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then sleep "${WB_TEST_CALL_DELAY:-0}"; echo '{"total_count":0,"workflow_runs":[]}'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then sleep "${WB_TEST_CALL_DELAY:-0}"; echo '{"total_count":0,"statuses":[]}'; exit 0; fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then sleep "${WB_TEST_CALL_DELAY:-0}"; echo '{"protected":false,"protection":{}}'; exit 0; fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then sleep "${WB_TEST_CALL_DELAY:-0}"; echo '[]'; exit 0; fi
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/'; then
  count=0
  [ -f "$WB_PULLS_COUNTER" ] && count=$(cat "$WB_PULLS_COUNTER")
  count=$((count + 1))
  printf '%s' "$count" > "$WB_PULLS_COUNTER"
  if [ "$count" -ge 6 ]; then
    # Every observation reads /pulls/17 three times, in this order:
    # pullRequestIdentity (top of the loop), commitChecks' own head-match
    # read, and finally requiredChecksReceipt's pullRequestTargetsBase --
    # the exact call this test targets. Only the SECOND observation's third
    # (6th overall) call is made to hang: its first two calls (4, 5) must
    # still succeed fast, or pullRequestIdentity's or commitChecks' own
    # earlier reason-handling returns first and this test would exercise
    # that branch instead of the one under test.
    #
    # Signal the test's already-blocked FIFO reader that this call has
    # begun, THEN hang. The test only flips its context to
    # DeadlineExceeded (and thereby kills this process) after receiving
    # that signal, so the rendezvous -- not a sleep duration on either
    # side -- is what decides timing.
    printf x > "$WB_TRIGGER_FIFO"
    # exec replaces this script's own process image with sleep, rather than
    # forking a child of it: Go's context cancellation kills exactly the
    # direct child process it started (this script's PID), and a forked
    # grandchild left holding the stdout/stderr pipe open would keep
    # CombinedOutput blocked on that pipe for the full sleep regardless of
    # the parent's death.
    exec sleep 30
  fi
  sleep "${WB_TEST_CALL_DELAY:-0}"
  echo '{"number":17,"state":"open","draft":false,"title":"candidate","head":{"ref":"candidate","sha":"` + head + `","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}'
  exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WB_PULLS_COUNTER", counter)
	t.Setenv("WB_TRIGGER_FIFO", fifo)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Opening a FIFO for reading blocks until a writer opens the same FIFO
	// (and vice versa): whichever side reaches its open() call first simply
	// waits for the other. Starting this goroutine before
	// WaitForCommitChecks is therefore enough to guarantee the rendezvous --
	// there is nothing further to synchronize on before making the call.
	triggerCtx := newTriggerContext(context.Background())
	go func() {
		file, err := os.OpenFile(fifo, os.O_RDONLY, 0)
		if err != nil {
			return
		}
		defer func() { _ = file.Close() }()
		buffer := make([]byte, 1)
		_, _ = file.Read(buffer)
		triggerCtx.trigger()
	}()

	result, err := WaitForCommitChecks(triggerCtx, PullRequestWaitOptions{
		Repository: "acme/app", PullRequest: "17", Target: "main", Head: head,
		AllowUnfenced: true, Slice: 5 * time.Minute, CheckPollInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WaitForCommitChecks: %v", err)
	}
	countBytes, readErr := os.ReadFile(counter)
	if readErr != nil {
		t.Fatalf("read pulls-call counter: %v", readErr)
	}
	if got := strings.TrimSpace(string(countBytes)); got != "6" {
		t.Fatalf("fake gh /pulls call count = %q, want exactly 6 -- a different code path was taken and this test no longer proves ciwait.go:224", got)
	}
	if result.Status != PullRequestWaitPending {
		t.Fatalf("result = %+v, want a pending receipt reusing the prior observation's reason", result)
	}
	if strings.Contains(result.Reason, "required-check authority is unavailable") {
		t.Fatalf("result.Reason = %q, must reuse the prior observation's reason rather than a fresh authority-unavailable one once the deadline is what killed the read", result.Reason)
	}
	if !strings.Contains(result.Reason, "terminal checks require one unchanged foreground reread") {
		t.Fatalf("result.Reason = %q, want the first observation's own terminal-pending-confirmation reason preserved", result.Reason)
	}
}
