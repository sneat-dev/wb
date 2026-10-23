package sessionmessenger

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSendRechecksDurableAuthorityAfterAcquiringExecutionLock(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"missing address", "changed address", "missing receipt"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			fixture := newSendFixture(t, sessionmove.CourierSSH)
			options := fixture.options(sessionmove.MessageKindText, "hello")
			calls := 0
			options.NewDeliverer = sdCovCountingFactory(&fakeMessageDeliverer{}, &calls)
			addressPath := filepath.Join(fixture.store.Root, "successors", fixture.address.SuccessorWBSessionID+".json")
			handoff := filepath.Join(fixture.store.Root, fixture.request.HandoffID)
			switch scenario {
			case "missing address":
				options.Hooks.AfterExecutionLock = func() {
					if err := os.Remove(addressPath); err != nil {
						t.Fatal(err)
					}
				}
			case "changed address":
				options.Hooks.AfterExecutionLock = func() {
					address := fixture.address
					ssh := *address.Route.SSH
					ssh.Host = "changed-target"
					address.Route.SSH = &ssh
					for path, value := range map[string]any{addressPath: address, filepath.Join(handoff, "route.json"): address.Route} {
						raw, err := json.MarshalIndent(value, "", "  ")
						if err != nil {
							t.Fatal(err)
						}
						raw = append(raw, '\n')
						if err := os.WriteFile(path, raw, 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			case "missing receipt":
				options.Hooks.AfterAddressLoad = func() {
					if err := os.Remove(filepath.Join(handoff, "receipt.json")); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := Send(context.Background(), options)
			if err == nil || calls != 0 {
				t.Fatalf("Send=%v,courier attempts=%d", err, calls)
			}
			if scenario == "missing address" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing address=%v", err)
			}
			if scenario == "changed address" && (!errors.Is(err, sessionmove.ErrHandoffConflict) || !strings.Contains(err.Error(), "address changed")) {
				t.Fatalf("changed address=%v", err)
			}
			if scenario == "missing receipt" && !strings.Contains(err.Error(), "no durable completed handoff receipt") {
				t.Fatalf("missing receipt=%v", err)
			}
		})
	}
}
