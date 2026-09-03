package streams

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// EventSchemaVersion is the stream event-log format this binary writes.
const EventSchemaVersion = 1

// Event is one structured record appended by a stream verb.
//
// `every-verb-appends-a-structured-event` is the requirement; the fields here
// are the minimum every P0 verb can populate truthfully. The log is
// append-only, versioned, redacted before any bytes are written, and safe for
// concurrent appenders.
type Event struct {
	SchemaVersion int       `json:"schema_version"`
	Timestamp     time.Time `json:"timestamp"`
	Stream        string    `json:"stream"`
	Verb          string    `json:"verb"`
	// Phase is the step inside the verb — "preflight", "worktree",
	// "pull-request", "link", "unlink", "lease" — so a timeline can show
	// where a verb spent its time rather than only that it ran.
	Phase string `json:"phase,omitempty"`
	// Repository qualifies the event when it is about one member.
	Repository string `json:"repository,omitempty"`
	// Outcome is the envelope vocabulary: success, findings, refused.
	Outcome string `json:"outcome"`
	// RefusalCode is set only when Outcome is "refused". It is the stable
	// identifier a caller branches on.
	RefusalCode string `json:"refusal_code,omitempty"`
	// Detail is human-readable and redacted.
	Detail string `json:"detail,omitempty"`
	// DurationMS is the wall time of the phase, when the verb measured it.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Evidence carries the exact facts the verb relied on. Values are
	// redacted before the event is written.
	Evidence map[string]string `json:"evidence,omitempty"`
}

// EventAppender is the seam between a stream verb and the event log.
//
// LANE SEAM: the P0 delivery order puts the shared event-log implementation in
// the row that also owns the exit-code/JSON envelope contract. Stream verbs
// depend on this interface, never on a concrete writer, so the shared
// implementation can replace FileEventLog without touching a verb. Nothing
// below the interface is part of a verb's contract.
type EventAppender interface {
	Append(event Event) error
}

// FileEventLog is the append-only JSONL log beside a stream's state.
//
// Concurrent appends are safe: each append takes an exclusive lock on the log
// and writes one already-encoded line, so two verbs running at once interleave
// records rather than corrupt one.
type FileEventLog struct {
	Path string
	Now  func() time.Time
}

// EventLog returns the log for one stream in this store.
func (store *Store) EventLog(name string) *FileEventLog {
	return &FileEventLog{Path: filepath.Join(store.Dir(name), "events.jsonl"), Now: store.Now}
}

// Append writes one redacted event. A failure to record an event never fails
// the verb's own work — callers log it — but it is reported rather than
// swallowed here.
func (log *FileEventLog) Append(event Event) error {
	event.SchemaVersion = EventSchemaVersion
	if event.Timestamp.IsZero() {
		if log.Now != nil {
			event.Timestamp = log.Now().UTC()
		} else {
			event.Timestamp = time.Now().UTC()
		}
	}
	event = Redact(event)
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode stream event: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(log.Path), 0o700); err != nil {
		return fmt.Errorf("create stream event directory: %w", err)
	}
	file, err := os.OpenFile(log.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open stream event log %s: %w", log.Path, err)
	}
	defer func() { _ = file.Close() }()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock stream event log: %w", err)
	}
	defer func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }()
	if _, err := file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append stream event: %w", err)
	}
	return nil
}

// ReadEvents parses one stream's event log. A record from a newer schema is
// refused rather than partially interpreted.
func ReadEvents(path string) ([]Event, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read stream event log %s: %w", path, err)
	}
	var events []Event
	for index, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("parse stream event log %s line %d: %w", path, index+1, err)
		}
		if event.SchemaVersion > EventSchemaVersion {
			return nil, fmt.Errorf("stream event log %s line %d uses schema version %d; this wb supports %d", path, index+1, event.SchemaVersion, EventSchemaVersion)
		}
		events = append(events, event)
	}
	return events, nil
}

// DiscardEvents is an appender that records nothing. It exists so a verb can
// run in a context with no stream directory (a dry preflight, a unit test)
// without special-casing a nil appender at every call site.
type DiscardEvents struct{}

// Append implements EventAppender.
func (DiscardEvents) Append(Event) error { return nil }

// redactionPatterns are applied to every string an event carries before it is
// written. `redaction-runs-before-any-bytes-leave-the-process` makes this a
// write-time guarantee, not an export-time filter: an event log that contains
// a token has already leaked it to whatever backs up the home directory.
var redactionPatterns = []*regexp.Regexp{
	// GitHub tokens of every documented prefix.
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	// npm automation tokens.
	regexp.MustCompile(`npm_[A-Za-z0-9]{20,}`),
	// Anything spelled as a secret assignment.
	regexp.MustCompile(`(?i)\b(token|secret|password|api[_-]?key)\b\s*[:=]\s*\S+`),
	// Bearer credentials and URL-embedded credentials.
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{12,}`),
	regexp.MustCompile(`(?i)https?://[^/\s:@]+:[^/\s@]+@`),
}

// RedactString removes credential-shaped substrings.
func RedactString(value string) string {
	redacted := value
	for _, pattern := range redactionPatterns {
		redacted = pattern.ReplaceAllString(redacted, "[redacted]")
	}
	return redacted
}

// Redact returns a copy of the event with every free-text field redacted.
func Redact(event Event) Event {
	event.Detail = RedactString(event.Detail)
	event.RefusalCode = RedactString(event.RefusalCode)
	if len(event.Evidence) == 0 {
		return event
	}
	evidence := make(map[string]string, len(event.Evidence))
	for key, value := range event.Evidence {
		evidence[key] = RedactString(value)
	}
	event.Evidence = evidence
	return event
}
