package sessionreceive

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceiveRefusesRequestLostAfterExecutionLock(t *testing.T) {
	t.Parallel()
	request, raw, _ := receiveTestRequest(t)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	_, err := Receive(context.Background(), Options{Store: store, LocalMachine: request.TargetMachine, RawRequest: raw, hooks: receiveHooks{afterExecutionLock: func() {
		if err := os.Remove(filepath.Join(store.Root, request.HandoffID, "request.json")); err != nil {
			t.Fatal(err)
		}
	}}})
	if err == nil {
		t.Fatal("accepted missing retained request")
	}
}

func TestLaunchOptionsRefusesInvalidWorkLogIdentity(t *testing.T) {
	t.Parallel()
	request, _, digest := receiveTestRequest(t)
	for _, invalid := range []string{"request", "digest"} {
		t.Run(invalid, func(t *testing.T) {
			t.Parallel()
			candidate, candidateDigest := request, digest
			if invalid == "request" {
				candidate.WorkLogReference = "invalid"
			} else {
				candidateDigest = "invalid"
			}
			if _, err := receiveLaunchOptions(Options{}, candidate, candidateDigest, nil, t.TempDir(), time.Now()); err == nil {
				t.Fatal("accepted invalid lineage")
			}
		})
	}
}

func TestReceiveFailureDiagnosticRemainsBoundedAndPrivate(t *testing.T) {
	t.Parallel()
	private := strings.Repeat("private source material", maxFailureDiagnosticBytes)
	for _, failure := range []error{errors.New(private), &sessionlaunch.AttemptFailureError{Evidence: sessionlaunch.FailureEvidence{Diagnostic: private}}} {
		diagnostic := receiveFailureDiagnostic(failure)
		if diagnostic == "" || len(diagnostic) > maxFailureDiagnosticBytes || strings.Contains(diagnostic, "private source material") {
			t.Fatalf("unsafe diagnostic: %q", diagnostic)
		}
	}
}
