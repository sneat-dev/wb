package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

//nolint:paralleltest // newRemoteFixture changes Git identity/config and WB_PROJECTS_ROOT process environment; subcases reuse one private store sequentially.
func TestRetireRemoteOwnershipUsesActualPrivateStoreAndLoginErrors(t *testing.T) {
	fixture := newRemoteFixture(t, "laptop")
	at := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	deps := fixture.deps("alice", at)
	ctx := context.Background()
	t.Run("controlled login failure", func(t *testing.T) {
		sentinel := errors.New("controlled login observation refused")
		failed := deps
		failed.login = func() (string, error) { return "", sentinel }
		err := retireCheckRemoteOwnership(ctx, failed, fixture.projectsRoot, "retire-task")
		if !errors.Is(err, sentinel) || err.Error() != "determine remote task owner: "+sentinel.Error() {
			t.Fatalf("controlled login error=%v", err)
		}
	})
	t.Run("controlled empty login", func(t *testing.T) {
		empty := deps
		empty.login = func() (string, error) { return "", nil }
		err := retireCheckRemoteOwnership(ctx, empty, fixture.projectsRoot, "retire-task")
		if err == nil || err.Error() != "determine remote task owner: empty login" {
			t.Fatalf("empty login error=%v", err)
		}
	})
	t.Run("actual unrelated durable claim remains", func(t *testing.T) {
		_, provider, err := loadRemote(deps, fixture.projectsRoot)
		if err != nil {
			t.Fatal(err)
		}
		claim := remotestate.Claim{SchemaVersion: remotestate.ClaimSchemaVersion, Task: "other-task", Login: "other-user", Machine: "other-machine", ClaimedAt: at, Note: "private unrelated custody receipt"}
		acquired, err := provider.Claim(ctx, claim, remotestate.ClaimNormal, "")
		if err != nil || acquired.Kind != remotestate.ClaimAcquired {
			t.Fatalf("actual private claim acquisition=%+v error=%v", acquired, err)
		}
		before, err := remotestate.ReadStatus(ctx, provider)
		if err != nil {
			t.Fatal(err)
		}
		if len(before.Claims) != 1 || before.Claims[0].Error != "" || !reflect.DeepEqual(before.Claims[0].Claim, claim) {
			t.Fatalf("persisted claim before retirement=%+v", before.Claims)
		}
		if err := retireCheckRemoteOwnership(ctx, deps, fixture.projectsRoot, "retire-task"); err != nil {
			t.Fatalf("unrelated actual claim blocked retirement: %v", err)
		}
		after, err := remotestate.ReadStatus(ctx, provider)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after.Claims, before.Claims) {
			t.Fatalf("retirement changed unrelated persisted claim: before=%+v after=%+v", before.Claims, after.Claims)
		}
	})
}
