package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteStatusOutputYAMLFormatSucceeds(t *testing.T) {
	t.Parallel()
	if err := writeStatusOutput(statusIndex{SchemaVersion: 1}, "yaml", "", false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteStatusOutputJSONFormatSucceeds(t *testing.T) {
	t.Parallel()
	if err := writeStatusOutput(statusIndex{SchemaVersion: 1}, "json", "", false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// With no heartbeat configured, start must close the stopped channel itself
// instead of relying on a background renderer, so finish can proceed
// straight through to the final summary line without ever starting one.
func TestStatusProgressStartWithoutHeartbeatClosesStoppedDirectly(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newStatusProgressWithHeartbeat(&out, true, 0)
	progress.start(2)
	progress.finish()
	if !strings.Contains(out.String(), "status: inspected 0 repositories in") {
		t.Fatalf("want a finish summary in output, got %q", out.String())
	}
}
