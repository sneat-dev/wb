package cmdworktree

import (
	"bytes"
	"encoding/json"
	"github.com/sneat-dev/wb/internal/worktrees"
	"strings"
	"testing"
)

func TestCwWtEncodeLogVerbResult(t *testing.T) {
	t.Parallel()
	result := worktrees.LogVerbResult{
		Verb: "steer", Worktree: "/tmp/wt", Applied: true, Prompt: "prompt-1",
		Event:   &worktrees.LocalWorkLogEvent{Type: "prompt_recorded", Seq: 3},
		Offline: true, Outbox: 2,
		Notes: []string{"note one"}, Diagnosis: []string{"line one"},
	}
	var out bytes.Buffer
	if err := writeJournalResult(&out, "text", result); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"steer /tmp/wt applied=true", "prompt=prompt-1",
		"event=prompt_recorded#3", "offline outbox=2",
		"- note one", "diagnosis: line one",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("log verb text missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	if err := writeJournalResult(&out, "json", result); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("log verb JSON: %v\n%s", err, out.String())
	}
	if decoded["verb"] != "steer" || decoded["prompt"] != "prompt-1" {
		t.Fatalf("log verb JSON = %+v", decoded)
	}

	// A minimal result writes only the header line.
	out.Reset()
	if err := writeJournalResult(&out, "text", worktrees.LogVerbResult{Verb: "sync"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "sync  applied=false\n" {
		t.Fatalf("minimal log verb = %q", got)
	}

	for allow := 0; allow < 7; allow++ {
		if err := writeJournalResult(&activeLimitedWriter{Allow: allow}, "text", result); err == nil {
			t.Fatalf("encodeLogVerbResult with %d writes allowed returned nil", allow)
		}
	}
}

func TestCwWtWorktreeLogPath(t *testing.T) {
	t.Parallel()
	if got := journalPath(nil); got != "." {
		t.Fatalf("no arg = %q", got)
	}
	if got := journalPath([]string{"/tmp/wt"}); got != "/tmp/wt" {
		t.Fatalf("one arg = %q", got)
	}
}

func TestJournalOriginalWriterSweep(t *testing.T) {
	t.Parallel()
	verb := worktrees.LogVerbResult{
		Verb: "steer", Worktree: "/tmp/wt", Applied: true, Prompt: "p",
		Event:   &worktrees.LocalWorkLogEvent{Type: "prompt_recorded", Seq: 1},
		Offline: true, Outbox: 1, Notes: []string{"n"}, Diagnosis: []string{"d"},
	}
	activeSweepWrites(t, 10, func(writer *activeLimitedWriter) error {
		return writeJournalResult(writer, "text", verb)
	})
}
