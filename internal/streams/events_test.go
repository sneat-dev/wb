package streams

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// REQ: every-verb-appends-a-structured-event — every append is versioned and
// concurrent appenders interleave records rather than corrupt one.
func TestEventLogAppendsConcurrentlyWithoutLosingRecords(t *testing.T) {
	log := &FileEventLog{Path: filepath.Join(t.TempDir(), "events.jsonl")}
	var group sync.WaitGroup
	const appenders = 16
	group.Add(appenders)
	for index := 0; index < appenders; index++ {
		go func(index int) {
			defer group.Done()
			_ = log.Append(Event{Stream: "s", Verb: "stream start", Outcome: "success"})
		}(index)
	}
	group.Wait()
	events, err := ReadEvents(log.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != appenders {
		t.Fatalf("events = %d, want %d", len(events), appenders)
	}
	for _, event := range events {
		if event.SchemaVersion != EventSchemaVersion {
			t.Fatalf("event schema version = %d, want %d", event.SchemaVersion, EventSchemaVersion)
		}
		if event.Timestamp.IsZero() {
			t.Fatal("event carries no timestamp")
		}
	}
}

// REQ: redaction-runs-before-any-bytes-leave-the-process — a credential never
// reaches the log file, not merely never reaches an export.
func TestEventLogRedactsBeforeWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	log := &FileEventLog{Path: path, Now: func() time.Time { return time.Unix(0, 0).UTC() }}
	secret := "ghp_0123456789abcdefghijklmnopqrstuvwx"
	if err := log.Append(Event{
		Stream: "s", Verb: "stream start", Outcome: "refused",
		Detail:   "gh failed with " + secret,
		Evidence: map[string]string{"remote": "https://octocat:" + secret + "@github.com/acme/app.git"},
	}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), secret) {
		t.Fatal("the event log file contains the credential")
	}
	if !strings.Contains(string(contents), "[redacted]") {
		t.Fatalf("the event was not redacted: %s", contents)
	}
}

func TestReadEventsRefusesANewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(`{"schema_version":99,"verb":"x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvents(path); err == nil {
		t.Fatal("a newer event schema was accepted")
	}
}

func TestReadEventsOnAMissingLogIsEmptyNotAnError(t *testing.T) {
	events, err := ReadEvents(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil || len(events) != 0 {
		t.Fatalf("events = %v, err = %v; want an empty log", events, err)
	}
}

func TestRedactStringCoversTheDocumentedCredentialShapes(t *testing.T) {
	for _, secret := range []string{
		"ghp_0123456789abcdefghijklmnopqrstuvwx",
		"github_pat_11ABCDEFG0abcdefghijkl_0123456789",
		"npm_0123456789abcdefghijklmnopqrstuvwx",
		"token: super-secret-value",
		"Authorization: Bearer abcdef0123456789",
	} {
		if redacted := RedactString(secret); strings.Contains(redacted, "secret") && !strings.Contains(redacted, "[redacted]") {
			t.Errorf("RedactString(%q) = %q", secret, redacted)
		} else if !strings.Contains(redacted, "[redacted]") {
			t.Errorf("RedactString(%q) = %q, want a redaction", secret, redacted)
		}
	}
}

func TestDiscardEventsAcceptsEverything(t *testing.T) {
	if err := (DiscardEvents{}).Append(Event{}); err != nil {
		t.Fatal(err)
	}
}
