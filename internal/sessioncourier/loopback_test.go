package sessioncourier

import (
	"context"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
)

func TestLoopbackDelivererRequiresLocalMachine(t *testing.T) {
	t.Parallel()
	_, err := (LoopbackDeliverer{}).Deliver(context.Background(), []byte("{}"))
	if err == nil || err.Error() != "loopback courier requires this machine's validated remote.machine" {
		t.Fatalf("error = %v", err)
	}
}

func TestLoopbackDelivererInvokesReceiveInProcess(t *testing.T) {
	t.Parallel()
	called := false
	deliverer := LoopbackDeliverer{
		LocalMachine: "laptop",
		ProjectsRoot: "/projects",
		Store:        sessionmove.NewStore(t.TempDir()),
		Receive: func(_ context.Context, options sessionreceive.Options) (sessionreceive.Result, error) {
			called = true
			if options.LocalMachine != "laptop" || options.ProjectsRoot != "/projects" || string(options.RawRequest) != `{"ok":true}` {
				t.Fatalf("receive options = %#v", options)
			}
			return sessionreceive.Result{Phase: sessionmove.PhaseCompleted}, nil
		},
	}
	result, err := deliverer.Deliver(context.Background(), []byte(`{"ok":true}`))
	if err != nil || !called || result.Phase != sessionmove.PhaseCompleted {
		t.Fatalf("result = %#v err=%v called=%v", result, err, called)
	}
}
