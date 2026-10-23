package sessionlaunch

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// unencodableTime is past encoding/json's time.Time.MarshalJSON year bound
// ([0, 9999]), so any struct that embeds it as a field fails to encode with
// a "year outside of range" error.
var unencodableTime = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)

const wantEncodingErrorSubstring = "year outside of range"

// TestSaveReadyReportsEncodingFailure drives saveReady's encodeLaunchJSON
// error branch (state.go): record.StartedAt flows straight into the
// launcherReady it encodes, with no range check before that call.
func TestSaveReadyReportsEncodingFailure(t *testing.T) {
	_, _, attempt, plan, planDigest := slCovAttempt(t)
	record := slCovReadyRecord(plan, 9300, unencodableTime)
	if _, err := attempt.saveReady(plan, planDigest, record); err == nil ||
		!strings.Contains(err.Error(), wantEncodingErrorSubstring) {
		t.Fatalf("saveReady(unencodable started_at) = %v, want a %q error", err, wantEncodingErrorSubstring)
	}
}

// TestSaveAbandonmentReportsEncodingFailure drives saveAbandonment's
// encodeLaunchJSON error branch (state.go): `now` is a direct parameter
// that flows straight into AbandonedAt with no range check, so a caller
// that supplies a year outside [0, 9999] reaches the encode call and fails
// -- a real, reachable failure, no new production seam needed.
func TestSaveAbandonmentReportsEncodingFailure(t *testing.T) {
	_, _, attempt, plan, planDigest := slCovAttempt(t)
	const pid = 9301
	fence, err := attempt.acquireExecFence(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := attempt.saveAbandonment(plan, planDigest, pid, unencodableTime); err == nil ||
		!strings.Contains(err.Error(), wantEncodingErrorSubstring) {
		t.Fatalf("saveAbandonment(unencodable now) = %v, want a %q error", err, wantEncodingErrorSubstring)
	}
}

// TestSaveExecFailureReportsEncodingFailure drives saveExecFailure's
// encodeLaunchJSON error branch (state.go): `now` is a direct parameter
// that flows straight into FailedAt with no range check.
func TestSaveExecFailureReportsEncodingFailure(t *testing.T) {
	_, _, attempt, plan, planDigest := slCovAttempt(t)
	const pid = 9302
	if err := attempt.saveExecFailure(plan, planDigest, "", "", pid, errors.New("launcher exec failed"), unencodableTime); err == nil ||
		!strings.Contains(err.Error(), wantEncodingErrorSubstring) {
		t.Fatalf("saveExecFailure(unencodable now) = %v, want a %q error", err, wantEncodingErrorSubstring)
	}
}

// TestSaveReleaseReportsEncodingFailure drives saveRelease's
// encodeLaunchJSON error branch (state.go): `now` is a direct parameter
// that flows straight into ReleasedAt with no range check.
func TestSaveReleaseReportsEncodingFailure(t *testing.T) {
	_, _, attempt, plan, planDigest := slCovAttempt(t)
	record := slCovReadyRecord(plan, 9303, time.Now())
	if _, err := attempt.saveReady(plan, planDigest, record); err != nil {
		t.Fatal(err)
	}
	fence, err := attempt.acquireExecFence(record.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fence.Close() }()
	ready := slCovReady(plan, attempt, planDigest, record)
	if _, _, err := attempt.saveRelease(plan, planDigest, ready, "", unencodableTime); err == nil ||
		!strings.Contains(err.Error(), wantEncodingErrorSubstring) {
		t.Fatalf("saveRelease(unencodable now) = %v, want a %q error", err, wantEncodingErrorSubstring)
	}
}

// TestSaveStartedReportsEncodingFailure drives saveStarted's
// encodeLaunchJSON error branch (state.go): `now` is a direct parameter
// that flows straight into StartedAt with no range check, and saveStarted
// itself validates nothing about its callers' prior artifacts before
// encoding.
func TestSaveStartedReportsEncodingFailure(t *testing.T) {
	state, _, attempt, plan, planDigest := slCovAttempt(t)
	release := launcherRelease{PID: 9304}
	if _, _, err := state.saveStarted(attempt, plan, planDigest, "", release, unencodableTime); err == nil ||
		!strings.Contains(err.Error(), wantEncodingErrorSubstring) {
		t.Fatalf("saveStarted(unencodable now) = %v, want a %q error", err, wantEncodingErrorSubstring)
	}
}
