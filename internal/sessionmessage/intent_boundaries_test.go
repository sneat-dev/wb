package sessionmessage

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceiveFailsClosedWhenIntentAppearsAfterRecipientVerification(t *testing.T) {
	t.Parallel()
	for _, corrupt := range []bool{false, true} {
		name := "exact existing intent"
		if corrupt {
			name = "corrupt existing intent"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newReceiveFixture(t)
			instant := time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC)
			fixture.options.Now = func() time.Time { return instant }
			fixture.options.RecordReceived = func(record WorkLogRecord) error {
				intent := sessionmove.MessagePasteIntent{SchemaVersion: sessionmove.MessagePasteIntentSchemaVersion, MessageID: fixture.message.MessageID, MessageDigest: sessionmove.DigestBytes(fixture.raw), HandoffID: fixture.request.HandoffID, RecipientWBSessionID: fixture.message.RecipientWBSessionID, TmuxName: fixture.receipt.TmuxName, PaneID: fixture.tmux.pane.ID, PID: fixture.receipt.PID, IntendedAt: instant}
				raw, err := json.MarshalIndent(intent, "", "  ")
				if err != nil {
					return err
				}
				raw = append(raw, '\n')
				if corrupt {
					raw = []byte("{}")
				}
				return os.WriteFile(filepath.Join(fixture.store.Root, fixture.request.HandoffID, "inbox", fixture.message.MessageID, "paste-intent.json"), raw, 0600)
			}
			_, err := Receive(context.Background(), fixture.options)
			if err == nil {
				t.Fatal("unexpected intent was accepted")
			}
			if !corrupt && !errors.Is(err, ErrMessagePasteAmbiguous) {
				t.Fatalf("replay error=%v", err)
			}
			if got := countCallPrefix(fixture.tmux.calls, "paste:"); got != 0 {
				t.Fatalf("pasted %d times", got)
			}
		})
	}
}

func TestNewOSTmuxReportsExecutableDisappearingAfterResolution(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tmux")
	missing := &os.PathError{Op: "stat", Path: path, Err: os.ErrNotExist}
	client, err := newOSTmuxWithResolver(func(name string) (string, error) {
		if name != "tmux" {
			t.Fatalf("resolver name=%q", name)
		}
		return path, nil
	}, func(got string) (os.FileInfo, error) {
		if got != path {
			t.Fatalf("resolved path=%q", got)
		}
		return nil, missing
	})
	if client != nil || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "inspect fixed tmux executable") {
		t.Fatalf("client=%+v,error=%v", client, err)
	}
}

func TestReceiveRejectsDecodedTimestampThatCannotBeCanonicallyEncoded(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	fixture.options.RawMessage = []byte(strings.Replace(string(fixture.raw), fixture.message.SentAt.Format(time.RFC3339), "2026-08-25T12:00:00+24:00", 1))
	if _, err := sessionmove.DecodeMessage(fixture.options.RawMessage); err != nil {
		t.Fatalf("fixture timestamp no longer accepted by decoder: %v", err)
	}
	_, err := Receive(context.Background(), fixture.options)
	if err == nil || !strings.Contains(err.Error(), "timezone hour outside of range") {
		t.Fatalf("canonical encoding failure=%v", err)
	}
	if len(fixture.tmux.calls) != 0 {
		t.Fatalf("encoding failure called tmux: %v", fixture.tmux.calls)
	}
}

func TestReceiveRejectsDurableReceiptOutsideRequestLineage(t *testing.T) {
	t.Parallel()
	fixture := newReceiveFixture(t)
	receipt := fixture.receipt
	receipt.TargetMachine = "other-machine"
	raw, err := sessionmove.EncodeReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.store.Root, fixture.request.HandoffID, "receipt.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = Receive(context.Background(), fixture.options)
	if err == nil || !strings.Contains(err.Error(), "target_machine") {
		t.Fatalf("receipt lineage error=%v", err)
	}
	if len(fixture.tmux.calls) != 0 {
		t.Fatalf("invalid receipt called tmux: %v", fixture.tmux.calls)
	}
}
