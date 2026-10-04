package daemonruntime

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestControllerDurableCheckpointFailuresPreservePriorNativeState(t *testing.T) {
	t.Parallel()
	failure := errors.New("durable checkpoint refused")
	cases := []struct {
		operation, kind string
		at              int
	}{
		{"launch", "save", 1}, {"launch", "save", 2}, {"launch", "save", 3}, {"launch", "load", 1}, {"launch", "load", 2},
		{"stop", "save", 1}, {"stop", "save", 2}, {"stop", "load", 1},
		{"recover", "save", 1}, {"recover", "load", 1},
		{"status", "load", 1}, {"status", "save", 1},
		{"mark-owned", "load", 1}, {"mark-owned", "save", 1},
		{"mark-unchanged", "load", 1}, {"mark-unchanged", "save", 1},
		{"start", "load", 1},
		{"restart", "load", 1}, {"restart", "load", 2},
		{"public-stop", "load", 1},
	}
	for _, tc := range cases {
		t.Run(tc.operation+"/"+tc.kind+"/"+string(rune('0'+tc.at)), func(t *testing.T) {
			t.Parallel()
			root := cwWtDaemonRoot(t)
			deps := daemonTestDependencies(t, root)
			now := deps.Now()
			deps.Now = func() time.Time { return now }
			deps.Sleep = func(d time.Duration) { now = now.Add(d) }
			alive := true
			deps.Alive = func(pid int) bool { return alive && (pid == 900 || (tc.operation == "launch" && pid == 901)) }
			deps.Stop = func(int, daemon.Supervisor, string) error { alive = false; return nil }
			controller := NewController(deps, root)
			provenance, err := controller.Provenance()
			if err != nil {
				t.Fatal(err)
			}
			state := daemonTestState(t, root, DefaultListen, provenance, "owner", now.Add(-time.Minute))
			state.MarkReady(900, now)
			if tc.operation == "recover" {
				state.Status = daemon.StatusStarting
				state.PID = 0
				state.UpdatedAt = now.Add(-time.Minute)
				deps.Health = func(context.Context, string) error { return errors.New("not listening") }
				controller.deps = deps
			}
			if tc.operation == "status" {
				alive = false
			}
			if tc.operation == "recover" {
				if err := secureDaemonRuntime(root); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(mustDaemonPath(t, daemonLifecycleLockPath, root), nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := controller.writeLifecycleOwnerPID(700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.operation != "launch" {
				if err := controller.store.Save(state); err != nil {
					t.Fatal(err)
				}
			}
			before, found, err := controller.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			last, lastFound := before, found
			calls := 0
			load, save := controller.state.load, controller.state.save
			controller.state.load = func() (daemon.State, bool, error) {
				if tc.kind == "load" {
					calls++
					if calls == tc.at {
						return daemon.State{}, false, failure
					}
				}
				return load()
			}
			controller.state.save = func(s daemon.State) error {
				if tc.kind == "save" {
					calls++
					if calls == tc.at {
						return failure
					}
				}
				if err := save(s); err != nil {
					return err
				}
				last, lastFound = s, true
				return nil
			}
			ctx := context.Background()
			switch tc.operation {
			case "launch":
				_, err = controller.launch(ctx, nil, DefaultListen, provenance, "start", false)
			case "stop":
				_, err = controller.stop(ctx, state)
			case "recover":
				_, err = controller.RecoverLifecycleLock(ctx, true)
			case "status":
				_, err = controller.Status(ctx)
			case "mark-owned":
				err = controller.MarkStoppedIfOwned("owner")
			case "mark-unchanged":
				_, err = controller.markStoppedIfUnchanged(state, 900, "owner")
			case "start":
				_, err = controller.Start(ctx, DefaultListen)
			case "restart":
				_, err = controller.RestartWithProgress(ctx, false, nil, false)
			case "public-stop":
				_, err = controller.Stop(ctx)
			}
			if !errors.Is(err, failure) {
				t.Fatalf("checkpoint %d error = %v (calls %d)", tc.at, err, calls)
			}
			actual, actualFound, loadErr := controller.store.Load()
			if loadErr != nil || actualFound != lastFound || !reflect.DeepEqual(actual, last) {
				t.Fatalf("failed checkpoint changed prior receipt: actual=%+v last=%+v found=%t/%t err=%v", actual, last, actualFound, lastFound, loadErr)
			}
		})
	}
}
