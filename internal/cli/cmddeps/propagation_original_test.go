package cmddeps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/streams"
)

func cwDepsPropagateResultFixture() locallink.Result {
	return locallink.Result{
		Library: "/tmp/cw/library", LibraryRepository: "acme/library", ContentHash: "abc123", Dirty: true, Stream: "cw-stream",
		Identities: []streams.Identity{{Ecosystem: streams.EcosystemGo, Name: "github.com/acme/library", Manifest: "go.mod"}},
		Plan:       []string{"link github.com/acme/library into acme/app", "verify acme/app"},
		Consumers: []locallink.ConsumerResult{
			{Consumer: "/tmp/cw/app", Skipped: true, Reason: "declares none of the library's identities"},
			{Consumer: "/tmp/cw/site",
				Links:         []streams.Link{{Identity: "github.com/acme/library", Mechanism: streams.MechanismGoWork, PreviousVersion: "v1.2.3"}},
				SkippedChecks: []string{"no pnpm lockfile to freeze"},
				Notes:         []string{"a published package already supersedes the record"},
				Verification: &locallink.Verification{
					Statement:   "verified against unpublished github.com/acme/library at content-hash abc123 (dirty)",
					ActiveLinks: []string{"github.com/acme/library was v1.2.3"},
					Linked: locallink.VerificationRun{Passed: false, Command: "go test -p 1 ./...",
						Details: []string{"TestThing failed"}},
					PublishedBaseline: locallink.VerificationRun{Passed: true, Command: "GOWORK=off go build ./...",
						Details: []string{"baseline note"}},
					Passed: true,
				}},
		},
	}
}

func TestCwDepsPrintPropagateLocalTextAndJSON(t *testing.T) {
	t.Parallel()
	result := cwDepsPropagateResultFixture()
	var out bytes.Buffer
	if err := printPropagateLocal(&out, "text", result); err != nil {
		t.Fatalf("printPropagateLocal text: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"plan:",
		"  - link github.com/acme/library into acme/app",
		"library /tmp/cw/library at content-hash abc123 (dirty)",
		"  publishes go github.com/acme/library (go.mod)",
		"/tmp/cw/app",
		"  skipped: declares none of the library's identities",
		"  linked github.com/acme/library via go.work (was v1.2.3)",
		"  ? not checked: no pnpm lockfile to freeze",
		"verified against unpublished github.com/acme/library",
		"active link: github.com/acme/library was v1.2.3",
		"linked run: passed=false go test -p 1 ./...",
		"! TestThing failed",
		"published baseline: passed=true GOWORK=off go build ./...",
		"! baseline note",
		"a published package already supersedes the record",
		"while these links are live, do not run",
		"--to /tmp/cw/site --undo",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("propagate text missing %q:\n%s", want, text)
		}
	}
	// The footer must not invite an --undo for a skipped consumer.
	if strings.Contains(text, "--to /tmp/cw/app ") || strings.Contains(text, "--to /tmp/cw/app\n") {
		t.Errorf("a skipped consumer appeared in the undo hint:\n%s", text)
	}
	// A clean library and a failed run are both named.
	out.Reset()
	clean := result
	clean.Dirty = false
	if err := printPropagateLocal(&out, "text", result); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := printPropagateLocal(&out, "text", clean); err == nil && !strings.Contains(out.String(), "(clean)") {
		t.Errorf("clean state not reported:\n%s", out.String())
	}
	// A per-consumer error is printed and suppresses the undo footer: an
	// undo hint on a run that did not link everything is a wrong instruction.
	out.Reset()
	failed := cwDepsPropagateResultFixture()
	failed.Consumers = []locallink.ConsumerResult{{Consumer: "/tmp/cw/app", Errors: []string{"go.work could not be written"}}}
	if err := printPropagateLocal(&out, "text", failed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "! go.work could not be written") {
		t.Errorf("a consumer error was not printed:\n%s", out.String())
	}
	if strings.Contains(out.String(), "while these links are live") {
		t.Errorf("a failed run printed the undo footer:\n%s", out.String())
	}
	// JSON carries the structured result.
	out.Reset()
	if err := printPropagateLocal(&out, "json", result); err != nil {
		t.Fatalf("printPropagateLocal json: %v", err)
	}
	var decoded locallink.Result
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("propagate JSON: %v\n%s", err, out.String())
	}
	if decoded.Library != result.Library || len(decoded.Consumers) != len(result.Consumers) {
		t.Fatalf("decoded = %+v", decoded)
	}
	// A failed write is surfaced.
	if err := printPropagateLocal(cwDepsFailingWriter{}, "text", result); err == nil {
		t.Error("a failed text write must be surfaced")
	}
	if err := printPropagateLocal(cwDepsFailingWriter{}, "json", result); err == nil {
		t.Error("a failed json write must be surfaced")
	}
}

func TestCwDepsConsumerPathsSkipsTheUnlinked(t *testing.T) {
	t.Parallel()
	result := locallink.Result{Consumers: []locallink.ConsumerResult{
		{Consumer: "/tmp/a"},
		{Consumer: "/tmp/b", Skipped: true},
		{Consumer: "/tmp/c"},
	}}
	paths := consumerPaths(result)
	if len(paths) != 2 || paths[0] != "/tmp/a" || paths[1] != "/tmp/c" {
		t.Fatalf("consumerPaths = %v", paths)
	}
	if got := consumerPaths(locallink.Result{}); len(got) != 0 {
		t.Fatalf("consumerPaths of an empty result = %v", got)
	}
}

// errRwi01WriteAt is the sentinel rwi01FailAtWriter returns on its Nth write,
// letting a test target one specific fmt.Fprint* call's error-check branch.
var errRwi01WriteAt = errors.New("rwi01: write refused")

// rwi01FailAtWriter succeeds on every write except the Nth, so a test can
// prove that printPropagateLocal surfaces a write failure at each individual
// call site rather than only the first.
type rwi01FailAtWriter struct {
	n     int
	count int
}

func (w *rwi01FailAtWriter) Write(p []byte) (int, error) {
	w.count++
	if w.count == w.n {
		return 0, errRwi01WriteAt
	}
	return len(p), nil
}

// TestPrintPropagateLocalEachWriteFailureIsSurfaced drives a write
// failure at every distinct fmt.Fprint* call in printPropagateLocal's text
// path (plan lines, content-hash line, per-identity line, per-consumer
// header, skipped reason, links, skipped-checks, verification statement,
// active links, linked/baseline runs and their details, notes, and the
// closing undo-hint footer) and asserts the specific write error is
// returned rather than swallowed.
func TestPrintPropagateLocalEachWriteFailureIsSurfaced(t *testing.T) {
	t.Parallel()
	result := cwDepsPropagateResultFixture()
	// Call numbers correspond, in order, to: plan header(1), plan item(2,3),
	// content-hash(4), identity(5), consumer[0] header(6), consumer[0]
	// skipped(7), consumer[1] header(8), link(9), skipped-check(10),
	// verification statement(11), active link(12), linked run(13), linked
	// detail(14), published baseline(15), baseline detail(16), note(17),
	// footer(18). Call 1 is already covered elsewhere; 3 and 8 hit the same
	// branch as 2 and 6.
	for _, n := range []int{2, 4, 5, 6, 7, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18} {
		n := n
		t.Run(fmt.Sprintf("call_%d", n), func(t *testing.T) {
			t.Parallel()
			w := &rwi01FailAtWriter{n: n}
			err := printPropagateLocal(w, "text", result)
			if !errors.Is(err, errRwi01WriteAt) {
				t.Fatalf("printPropagateLocal write failure at call %d: err = %v, want errRwi01WriteAt", n, err)
			}
		})
	}
}

// TestPrintPropagateLocalConsumerErrorWriteFailureIsSurfaced targets
// the per-consumer failure line (result.Consumers[i].Errors), which the
// comprehensive fixture above never populates: a write failure on the
// second write (the failure line itself, after the consumer header) must
// be surfaced.
func TestPrintPropagateLocalConsumerErrorWriteFailureIsSurfaced(t *testing.T) {
	t.Parallel()
	failed := locallink.Result{Consumers: []locallink.ConsumerResult{
		{Consumer: "/tmp/cw/app", Errors: []string{"go.work could not be written"}},
	}}
	w := &rwi01FailAtWriter{n: 2}
	err := printPropagateLocal(w, "text", failed)
	if !errors.Is(err, errRwi01WriteAt) {
		t.Fatalf("printPropagateLocal consumer-error write failure: err = %v, want errRwi01WriteAt", err)
	}
}
