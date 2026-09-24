package sessionlaunch

import (
	"errors"
	"testing"
	"time"
)

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
	if _, _, err := attempt.saveRelease(plan, planDigest, ready, "", unencodableTime); err == nil {
		t.Fatal("saveRelease accepted a now year outside [0, 9999], want an encoding error")
	}
}

// unencodableTime is past encoding/json's time.Time.MarshalJSON year bound
// ([0, 9999]), so any struct that embeds it as a field fails to encode.
var unencodableTime = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)

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
	if _, _, err := attempt.saveAbandonment(plan, planDigest, pid, unencodableTime); err == nil {
		t.Fatal("saveAbandonment accepted a now year outside [0, 9999], want an encoding error")
	}
}

// TestSaveExecFailureReportsEncodingFailure drives saveExecFailure's
// encodeLaunchJSON error branch (state.go): `now` is a direct parameter
// that flows straight into FailedAt with no range check.
func TestSaveExecFailureReportsEncodingFailure(t *testing.T) {
	_, _, attempt, plan, planDigest := slCovAttempt(t)
	const pid = 9302
	if err := attempt.saveExecFailure(plan, planDigest, "", "", pid, errors.New("launcher exec failed"), unencodableTime); err == nil {
		t.Fatal("saveExecFailure accepted a now year outside [0, 9999], want an encoding error")
	}
}
