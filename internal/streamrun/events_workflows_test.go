package streamrun

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
)

func TestStreamEventSinkAppendsToTheStreamLog(t *testing.T) {
	store := streams.OpenAt(filepath.Join(t.TempDir(), "streams"))
	sink := streamEventSink{log: store.EventLog("cw-cov")}
	if err := sink.Append(streamsync.Event{Stream: "cw-cov", Verb: "sync", Phase: "rebase", Repository: "acme/app", Outcome: "ok", Detail: "rebased", Evidence: map[string]string{"head": "sha"}}); err != nil {
		t.Fatal(err)
	}
	events, err := streams.ReadEvents(store.EventLog("cw-cov").Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one recorded event", events)
	}
	if events[0].Stream != "cw-cov" || events[0].Verb != "sync" || events[0].Repository != "acme/app" ||
		events[0].Outcome != "ok" || len(events[0].Evidence) != 1 {
		t.Fatalf("recorded event = %+v", events[0])
	}
}

func TestWorkflowMechanismsReadsPullRequestWorkflows(t *testing.T) {
	empty := t.TempDir()
	present, opaque, err := workflowMechanisms{}.Present(empty)
	if err != nil {
		t.Fatal(err)
	}
	if len(present) != 0 || opaque {
		t.Fatalf("no-workflow dir = (%v, %v)", present, opaque)
	}

	root := t.TempDir()
	workflow := filepath.Join(root, ".github", "workflows", "stream.yml")
	if err := os.MkdirAll(filepath.Dir(workflow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflow, []byte(`name: stream
on:
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: go build ./...
      - run: go vet ./...
      - run: go test -count=1 ./...
`), 0o644); err != nil {
		t.Fatal(err)
	}
	present, _, err = workflowMechanisms{}.Present(root)
	if err != nil {
		t.Fatal(err)
	}
	if !present["go vet"] || !present["-count=1"] {
		t.Fatalf("a pull-request workflow with vet/count invocations reported %v", present)
	}
}
