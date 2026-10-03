package sessionrun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessioncourier"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultLocalMachineRejectsMalformedPrivateConfiguration(t *testing.T) {
	path := wbconfig.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("remote: ["), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if _, err := DefaultMoveDependencies().LocalMachine(); err == nil {
		t.Fatal("malformed config accepted")
	}
	if err := os.WriteFile(path, []byte("remote:\n  provider: git\n  repo: acme/state\n  machine: private-machine\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if machine, err := DefaultMoveDependencies().LocalMachine(); err != nil || machine != "private-machine" {
		t.Fatalf("machine=%s error=%v", machine, err)
	}
}

func TestMoveCourierAndDurabilityCallbacksKeepExactRefusals(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"local-via", "invalid-courier", "early-dispatch", "save-route", "save-dispatch", "response-codec", "expected-codec"} {
		t.Run(stage, func(t *testing.T) {
			store := sessionmove.NewStore(t.TempDir())
			source, request, _, digest := cwDepsMoveFixture(t, store)
			want := errors.New("callback sentinel")
			deps := MoveDependencies{LocalMachine: func() (string, error) { return "laptop", nil }, DefaultConfigPath: func() string { return "private" }, LoadConfig: func(string) (sessionmove.Config, error) { return cwDepsMoveConfig(cwDepsSSHTarget()), nil }}
			switch stage {
			case "local-via":
				_, err := NewMove(deps).Move(context.Background(), MoveRequest{Target: "laptop", Via: "ssh"}, nil)
				if err == nil || !strings.Contains(err.Error(), "loopback") {
					t.Fatal(err)
				}
			case "invalid-courier":
				_, err := NewMove(deps).Move(context.Background(), MoveRequest{Target: "hetzner-vm1", Via: "invalid"}, nil)
				if err == nil {
					t.Fatal("invalid courier accepted")
				}
			case "early-dispatch":
				deps.LoadConfig = func(string) (sessionmove.Config, error) {
					return cwDepsMoveConfig(sessionmove.TargetConfig{Machine: "hetzner-vm1", DefaultCourier: sessionmove.CourierSynchestra, Synchestra: &sessionmove.SynchestraConfig{Runner: "runner"}}), nil
				}
				deps.NewDeliverer = func(_ sessionmove.TargetConfig, _ sessionmove.Courier, options sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
					err := options.SaveDispatch(sessionmove.SynchestraDispatch{})
					if err == nil || !strings.Contains(err.Error(), "before Synchestra") {
						t.Fatalf("early dispatch=%v", err)
					}
					return nil, want
				}
				if _, err := NewMove(deps).Move(context.Background(), MoveRequest{Target: "hetzner-vm1"}, nil); !errors.Is(err, want) {
					t.Fatal(err)
				}
			case "save-route":
				deps.ResolveSource = func(string) (session.Record, bool, error) { return source, true, nil }
				deps.Store = func(string) (sessionmove.Store, error) { return store, nil }
				deps.LoadConfig = func(string) (sessionmove.Config, error) {
					if err := os.Mkdir(filepath.Join(store.Root, request.HandoffID, "route.json"), 0700); err != nil {
						t.Fatal(err)
					}
					return cwDepsMoveConfig(cwDepsSSHTarget()), nil
				}
				if _, err := executeResume(deps, request.HandoffID, ""); err == nil {
					t.Fatal("route write accepted")
				}
			case "save-dispatch":
				options, err := moveSynchestraOptions(store, request.HandoffID, sessionmove.CourierSynchestra)
				if err != nil {
					t.Fatal(err)
				}
				if err := options.SaveDispatch(sessionmove.SynchestraDispatch{}); err == nil {
					t.Fatal("invalid dispatch accepted")
				}
			case "response-codec", "expected-codec":
				delivery := sessionreceive.Result{Phase: sessionmove.PhaseCompleted, Request: request, Digest: digest, Receipt: &sessionmove.Receipt{}}
				if stage == "response-codec" {
					delivery.Request.HandoffID = ""
				} else {
					request.HandoffID = ""
				}
				if _, err := acknowledgeMove("", context.Background(), MoveDependencies{}, store, source, sessionmove.CourierSSH, request, digest, delivery); err == nil {
					t.Fatal("invalid canonical request accepted")
				}
			}
		})
	}
}
