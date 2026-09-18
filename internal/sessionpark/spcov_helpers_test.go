package sessionpark

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

// spCovBundleWithID returns one valid bundle with a distinct parked and source
// session identity so multiple aggregates can coexist in a single store root.
func spCovBundleWithID(t *testing.T, parkID, sourceID string) Bundle {
	t.Helper()
	bundle := testBundle(t)
	bundle.ParkedSessionID = parkID
	bundle.Source.WBSessionID = sourceID
	return bundle
}

// spCovStoreRoot creates one existing private 0700 store root. t.TempDir()
// itself is not 0700 on every platform, so an explicit root is required for
// every path that opens a pre-existing store.
func spCovStoreRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "store")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

// spCovCreatedStore creates one valid aggregate and returns the owning store.
func spCovCreatedStore(t *testing.T) (Store, Bundle) {
	t.Helper()
	store := NewStore(t.TempDir())
	bundle := testBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	return store, bundle
}

// spCovAcquire opens the durable source lock for a created aggregate and
// registers cleanup so a failing assertion can never leak the lock.
func spCovAcquire(t *testing.T, store Store, id string) *SourceLock {
	t.Helper()
	lock, err := store.Acquire(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return lock
}

// spCovWriteRaw writes exact bytes with an explicit permission so private-file
// validation paths can be exercised from hand-written artifacts.
func spCovWriteRaw(t *testing.T, path string, raw []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
}

// spCovAggregatePath is the private aggregate directory for one park identity.
func spCovAggregatePath(root, parkID string) string {
	return filepath.Join(root, parkID)
}

// spCovResumedEvent builds one well-formed source resumed event.
func spCovResumedEvent(sequence uint64, mutate func(*Event)) Event {
	event := Event{
		SchemaVersion: SchemaVersion, Sequence: sequence, Type: "resumed", At: time.Unix(150, 0).UTC(),
		Successor: &session.Record{PID: 7, WBSessionID: "wbs-succ", PredecessorWBSessionID: "wbs-source"},
	}
	if mutate != nil {
		mutate(&event)
	}
	return event
}

// spCovWriteEvent writes one hand-crafted source event file.
func spCovWriteEvent(t *testing.T, eventsDir string, event Event) {
	t.Helper()
	raw, err := jsonMarshal(event)
	if err != nil {
		t.Fatal(err)
	}
	spCovWriteRaw(t, filepath.Join(eventsDir, fmt.Sprintf("%020d.json", event.Sequence)), raw, 0o600)
}

// spCovReceiptFixture returns one receipt that satisfies the strict shape
// checks, so downstream identity-conflict branches can be reached.
func spCovReceiptFixture(resumeID string, digest sessionmove.Digest) Receipt {
	member := ReceiptMember{
		MemberID: "m-001-abcd", Repository: "acme/app", TargetPath: "/tmp/target",
		Pin: MemberPin(resumeID, "m-001-abcd"), Commit: strings.Repeat("a", 40),
		TargetWorkLogReference: "worklog:effort/run/" + strings.Repeat("b", 64),
	}
	return Receipt{
		SchemaVersion: ReceiptSchemaVersion, ResumeID: resumeID, RequestDigest: digest,
		ParkedSessionID: "park-test", SuccessorWBSessionID: "wbs-succ", PredecessorWBSessionID: "wbs-source",
		TargetMachine: "target", TmuxName: "wb-session-wbs-succ", Runtime: "codex",
		AttemptID: "000001-11111111111111111111111111111111", AttemptIndex: 1, PID: 4242,
		StartedAt: time.Unix(150, 0).UTC(), Members: []ReceiptMember{member},
	}
}

// spCovTargetFixture admits one target envelope and returns the store and
// admission for the target-side lock authority tests.
func spCovTargetFixture(t *testing.T) (TargetStore, TargetAdmission) {
	t.Helper()
	store := NewTargetStore(filepath.Join(t.TempDir(), "target-store"))
	admission, err := store.Admit(targetEnvelopeForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	return store, admission
}

// spCovDigest is a concise alias for one typed protocol digest.
func spCovDigest(value string) sessionmove.Digest { return sessionmove.Digest(value) }

// spCovEventName is the canonical 20-digit target event artifact name.
func spCovEventName(sequence uint64) string { return fmt.Sprintf("%020d.json", sequence) }

// spCovTargetReceipt builds a receipt bound to one admitted target envelope.
func spCovTargetReceipt(t *testing.T, admission TargetAdmission) Receipt {
	t.Helper()
	return validRemoteReceipt(t, RemoteAdmission{Envelope: admission.Envelope, Digest: admission.Digest})
}

// spCovTargetLock acquires the target execution lock for one admission.
func spCovTargetLock(t *testing.T, store TargetStore, admission TargetAdmission) *TargetLock {
	t.Helper()
	lock, err := store.Acquire(context.Background(), admission.Envelope.Request.ResumeID, admission.Digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return lock
}
