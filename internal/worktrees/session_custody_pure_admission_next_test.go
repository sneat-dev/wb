package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

func TestSessionCustodyNextPureAttachAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		predecessor string
		attempt     string
		want        string
	}{
		{"invalid predecessor", "another", "attempt-pure", "local parked successor does not descend from the parked source session"},
		{"unstable attempt", "source", "", "local parked successor requires one stable launcher attempt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// These are admission DTO values, not claimed owned process/session state.
			custody := &ParkedLocalCustody{bundle: sessionpark.Bundle{Source: session.Record{WBSessionID: "source"}}, beforeAttachAppend: func(*os.File) { t.Fatal("append reached before pure admission") }}
			successor := session.Record{PID: 1, WBSessionID: "successor", PredecessorWBSessionID: tc.predecessor, StartedAt: time.Unix(100, 0).UTC()}
			if err := custody.Attach(context.Background(), successor, tc.attempt, 1); err == nil || err.Error() != tc.want {
				t.Fatalf("admission=%v want %q", err, tc.want)
			}
			if len(custody.members) != 0 {
				t.Fatal("pure admission created retained members")
			}
		})
	}
}

func TestSessionCustodyNextPureBundleEncodingFailure(t *testing.T) {
	t.Parallel()
	source := session.Record{PID: 1, WBSessionID: "source", Machine: "machine", Runtime: "codex", StartedAt: time.Unix(100, 0).UTC()}
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-pure", Source: source, Continuation: "resume", ParkedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}
	// No members means the custody validation barrier does no native work. A
	// complete minimal envelope passes schema/identity guards to the time encoder.
	custody := &ParkedLocalCustody{bundle: bundle, beforeAttachAppend: func(*os.File) { t.Fatal("append reached after rejected encoding") }}
	successor := session.Record{PID: 1, WBSessionID: "successor", PredecessorWBSessionID: "source", StartedAt: source.StartedAt.Add(time.Minute)}
	_, controlErr := sessionpark.EncodeBundle(bundle)
	var controlCause *json.MarshalerError
	if !errors.As(controlErr, &controlCause) || controlCause.Err == nil || !strings.Contains(controlCause.Err.Error(), "year outside of range [0,9999]") {
		t.Fatalf("native bundle encoding prerequisite=%v", controlErr)
	}
	err := custody.Attach(context.Background(), successor, "attempt-pure", 1)
	var cause *json.MarshalerError
	if !errors.As(err, &cause) || cause.Type != controlCause.Type || cause.Err == nil || cause.Err.Error() != controlCause.Err.Error() {
		t.Fatalf("bundle timestamp error=%v", err)
	}
	if len(custody.members) != 0 {
		t.Fatal("encoding refusal created retained members")
	}
	if !custody.bundle.ParkedAt.Equal(bundle.ParkedAt) {
		t.Fatal("encoding refusal changed admitted timestamp")
	}
}

func TestSessionCustodyNextPureRemoteRepositoryAdmission(t *testing.T) {
	t.Parallel()
	bundle := sessionpark.Bundle{Worktrees: []sessionpark.Worktree{{Repository: "invalid"}}}
	called := false
	err := WithParkedRemoteResumeCustody(context.Background(), "", bundle, func() error { called = true; return nil })
	const want = "resolve canonical clone for invalid: parked member repository \"invalid\" is not owner/repository"
	if called || err == nil || err.Error() != want {
		t.Fatalf("repository admission=%v called=%v want %q", err, called, want)
	}
	if bundle.Worktrees[0].Repository != "invalid" {
		t.Fatal("admission rewrote source evidence")
	}
}
