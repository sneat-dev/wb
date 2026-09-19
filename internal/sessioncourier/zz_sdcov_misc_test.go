package sessioncourier

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/testenv"
)

// TestSDCovLoopbackFallsBackToInProcessReceive drives the default receive seam
// of a loopback courier that carries no injected receiver. An undecodable
// request must fail inside sessionreceive.Receive rather than panicking on a
// nil seam.
func TestSDCovLoopbackFallsBackToInProcessReceive(t *testing.T) {
	testenv.Isolate(t)
	deliverer := LoopbackDeliverer{
		LocalMachine: "laptop",
		ProjectsRoot: t.TempDir(),
		Store:        sessionmove.NewStore(t.TempDir()),
	}
	_, err := deliverer.Deliver(context.Background(), []byte("not-a-request"))
	if err == nil || !strings.Contains(err.Error(), "parse session move request") {
		t.Fatalf("loopback fallback error = %v, want in-process receive refusal", err)
	}
	if strings.Contains(err.Error(), "loopback courier requires") {
		t.Fatalf("loopback fallback returned the local-machine guard: %v", err)
	}
}

func TestSDCovDeliverSSHReportsConstructorFailure(t *testing.T) {
	t.Parallel()
	_, raw := courierTestRequest(t)
	_, err := DeliverSSH(context.Background(), sessionmove.SSHConfig{Host: "target;touch"}, raw)
	if err == nil || !strings.Contains(err.Error(), "must start with a letter or digit") {
		t.Fatalf("DeliverSSH error = %v, want config validation refusal", err)
	}
}
