package cmdrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/runexec"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/runqueue"
	"strings"
	"testing"
	"time"
)

func TestHistoryFormatsManagedSummaryAsTextAndJSON(t *testing.T) {
	t.Parallel()
	result := runexec.HistoryResult{Days: 14, Path: "/repo/.wb/local/run/events.jsonl", Summary: runlog.Summary{Operations: 2, Failed: 1, Kinds: []runlog.KindSummary{{Kind: "go test", Operations: 1, P50MS: 1000, P95MS: 1000, WallMS: 1000}}}}
	var out bytes.Buffer
	if err := printHistory(&out, result, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Governed commands · 14 days ·", "operations 2 · failed 1", "go test"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, out.String())
		}
	}
	out.Reset()
	if err := printHistory(&out, result, true); err != nil {
		t.Fatal(err)
	}
	var summary runlog.Summary
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil || summary.Operations != 2 || summary.Failed != 1 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	for _, at := range []int{1, 2, 3} {
		writer := &failAfterNWriter{failAt: at}
		if err := printHistory(writer, result, false); err == nil || writer.writes != at {
			t.Fatalf("write at %d: %v", at, err)
		}
	}
	sentinel := errors.New("JSON refused")
	if err := printHistory(failingWriter{sentinel}, result, true); err != sentinel {
		t.Fatal(err)
	}
}
func TestQueueRendersRunningWaitingAndJSONWithoutLiveFixture(t *testing.T) {
	t.Parallel()
	listing := runqueue.QueueListing{Budget: 4, HeavyK: 1}
	var out bytes.Buffer
	if err := printQueue(&out, listing, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"WB CPU queue", "running (", "waiting ("} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, out.String())
		}
	}
	out.Reset()
	if err := printQueue(&out, listing, true); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
}
func TestQueueEntryWriterErrorsAndJSONFailure(t *testing.T) {
	t.Parallel()
	listing := runqueue.QueueListing{Budget: 4, Running: []runqueue.QueueEntry{{PID: 7, Summary: "go build", Worktree: "/running", Units: 3, Age: time.Minute}}, Waiting: []runqueue.QueueEntry{{PID: 8, Summary: "go test", Worktree: "/waiting", Age: 3 * time.Second}}}
	var out bytes.Buffer
	if err := printQueue(&out, listing, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"running (1)", "waiting (1)", "go build", "go test", "/running", "/waiting", "1m0s", "3s"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, out.String())
		}
	}
	for _, at := range []int{3, 5} {
		writer := &failAfterNWriter{failAt: at}
		if err := printQueue(writer, listing, false); err == nil || writer.writes != at {
			t.Fatal(err)
		}
	}
	sentinel := errors.New("JSON refused")
	if err := printQueue(failingWriter{sentinel}, listing, true); err != sentinel {
		t.Fatal(err)
	}
}
