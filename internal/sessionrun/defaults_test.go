package sessionrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessioncourier"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmessenger"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkcourier"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestDefaultsKeepScannerHomeStoreAndClockAuthority(t *testing.T) {
	t.Parallel()
	if scanner, _, err := DefaultScanner(); err != nil || scanner == nil {
		t.Fatalf("scanner=%v error=%v", scanner, err)
	}
	want := errors.New("scanner loader")
	if _, err := ScanContinuation(func() (*secretscan.Scanner, []string, error) { return nil, nil, want }, nil); !errors.Is(err, want) {
		t.Fatal(err)
	}
	root := t.TempDir()
	store, err := ParkTargetStore(root)
	if err != nil || store.Root != filepath.Join(root, ".wb", sessionpark.TargetDirName) {
		t.Fatalf("store=%+v error=%v", store, err)
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(blocker, "root")
	if _, err := DirForWrite(bad); err == nil {
		t.Fatal("invalid directory accepted")
	}
	if _, err := MoveStore(bad); err == nil {
		t.Fatal("invalid move store accepted")
	}
	if _, err := ParkTargetStore(bad); err == nil {
		t.Fatal("invalid target store accepted")
	}
	if time.Since(DefaultMessageDependencies().Now()) > time.Second {
		t.Fatal("default clock stale")
	}
	source := session.Record{WBSessionID: "source"}
	for _, clock := range []func() time.Time{nil, DefaultMessageDependencies().Now} {
		var observed time.Time
		deps := MessageDependencies{Now: clock, ResolveSource: func(string) (session.Record, bool, error) { return source, true, nil }, Store: func(string) (sessionmove.Store, error) { return sessionmove.Store{}, nil }, NewMessageID: func() (string, error) { return "message", nil }, Send: func(_ context.Context, options sessionmessenger.Options) (sessionmessenger.Result, error) {
			observed = options.Now()
			return sessionmessenger.Result{}, nil
		}}
		if _, err := NewMessage(deps).Send(context.Background(), MessageRequest{Target: "target"}); err != nil {
			t.Fatal(err)
		}
		if observed.IsZero() || observed.Location() != time.UTC {
			t.Fatalf("clock=%v", observed)
		}
	}
}

func TestDefaultMoveAndReceiveFactoriesDelegateActualCourierPolicy(t *testing.T) {
	t.Parallel()
	deps := DefaultMoveDependencies()
	if _, err := deps.LocalMachine(); err == nil {
		t.Fatal("missing configured machine accepted")
	}
	if _, err := deps.NewDeliverer(sessionmove.TargetConfig{Machine: "target", Synchestra: &sessionmove.SynchestraConfig{Runner: "/absent-private-synchestra"}}, sessionmove.CourierSynchestra, sessioncourier.SynchestraOptions{}); err == nil {
		t.Fatal("missing actual runner accepted")
	}
	if receive := DefaultReceiveDependencies(); receive.LocalMachine == nil || receive.Store == nil || receive.Receive == nil {
		t.Fatal("receive default missing")
	}
	if park := DefaultReceiveParkDependencies(); park.LocalMachine == nil || park.Store == nil || park.Receive == nil {
		t.Fatal("park receive default missing")
	}
}

func TestDefaultResumeCallbacksKeepRealValidationAndFailureIdentity(t *testing.T) {
	t.Parallel()
	deps := DefaultResumeDependencies()
	if deps.Now().Location() != time.UTC {
		t.Fatal("clock not UTC")
	}
	if err := deps.PreflightLocal(session.Record{Runtime: "unsupported-private-runtime"}); err == nil {
		t.Fatal("invalid runtime accepted")
	}
	if _, err := deps.DeliverSSH(context.Background(), sessionmove.SSHConfig{Host: "invalid host"}, nil, sessionparkcourier.Options{}); err == nil {
		t.Fatal("invalid SSH config accepted")
	}
	if _, err := deps.DeliverSSH(context.Background(), sessionmove.SSHConfig{Host: "example.test"}, []byte("invalid envelope"), sessionparkcourier.Options{}); err == nil {
		t.Fatal("invalid envelope accepted")
	}
	if err := deps.AttachLocal(context.Background(), &worktrees.ParkedLocalCustody{}, session.Record{}, "", 0); err == nil {
		t.Fatal("invalid successor accepted")
	}
	if _, err := deps.InspectPrepared(context.Background(), sessionlaunch.Options{}); err == nil {
		t.Fatal("missing actual authority accepted")
	}
}
