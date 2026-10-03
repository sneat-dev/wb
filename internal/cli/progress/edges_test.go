package progress

import (
	"bytes"
	"errors"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"io"
	"strings"
	"testing"
)

type refusedWriter struct{}

func (refusedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestLiveProgressNilAndElapsedAndPadding(t *testing.T) {
	t.Parallel()
	var p *Live
	p.Start("a")
	p.Update("b")
	p.Finish("c")
	p.PrintLine("d")
	var out bytes.Buffer
	p = NewLiveWithHeartbeat(&out, true, 0)
	if p.withElapsed("cold") != "cold" {
		t.Fatal("cold elapsed")
	}
	p.Start("longer text")
	p.Update("x")
	p.PrintLine("immediate")
	p.Finish("done")
	p.Finish("again")
	if !strings.Contains(out.String(), "immediate\n") {
		t.Fatal(out.String())
	}
	disabled := NewLive(&out, false)
	before := out.Len()
	disabled.PrintLine("ignored")
	if out.Len() != before {
		t.Fatal("disabled wrote")
	}
}
func TestLineOutputPreservesPayloadAndPropagatesFailure(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	writer := Output(&out, false)
	for _, s := range []string{"\rfirst", "\rsecond", "\n", "\ragain", "plain"} {
		if n, err := writer.Write([]byte(s)); err != nil || n != len(s) {
			t.Fatalf("n=%d err=%v", n, err)
		}
	}
	if out.String() != "first\nsecond\nagainplain" {
		t.Fatal(out.String())
	}
	if n, err := Output(refusedWriter{}, false).Write([]byte("\ra")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
func TestCheckProgressIdentityAndFailureBuckets(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ pr, target, head, want string }{{"", "main", "short", "main@short"}, {"42", "", "", "PR 42"}, {"", "", "", "acme/app"}} {
		var out bytes.Buffer
		p := NewChecksWithHeartbeat(&out, true, 0)
		p.Start("acme/app", test.pr, test.target, test.head)
		p.Report(orchestrate.PullRequestWaitProgress{Observation: 1, Result: orchestrate.PullRequestWaitResult{Checks: []orchestrate.RemoteCheck{{Name: "skip", Bucket: "skipping"}, {Name: "cancel", Bucket: "cancel"}, {Name: "fail", Bucket: "fail"}}}})
		p.Fail(nil)
		if !strings.Contains(out.String(), test.want) || !strings.Contains(out.String(), "2 failed") {
			t.Fatal(out.String())
		}
	}
}
func TestEnabledAndCallerUpdate(t *testing.T) {
	t.Parallel()
	var nilLive *Live
	if nilLive.Enabled() || NewLive(io.Discard, false).Enabled() || !NewLive(io.Discard, true).Enabled() {
		t.Fatal("enabled contract")
	}
	var out bytes.Buffer
	p := NewChecksWithHeartbeat(&out, true, 0)
	p.Update("caller message")
	p.FinishOperation("done")
	if !strings.Contains(out.String(), "caller message") {
		t.Fatal(out.String())
	}
}
