package sessioncustody

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestAcknowledgeCorroboratesPublishedAddressBeforeSourceSeal(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"publication hook fails", "address disappears", "route and address change"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			fixture := newCustodyFixture(t)
			options := fixture.options
			options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
				t.Fatal("uncorroborated address reached source seal")
				return worktrees.ExternalSourceSealResult{}, nil
			}
			failure := errors.New("publication boundary interrupted")
			options.Hooks.AfterAddressPublished = func() error {
				path := filepath.Join(fixture.store.Root, "successors", fixture.request.SuccessorWBSessionID+".json")
				switch scenario {
				case "publication hook fails":
					return failure
				case "address disappears":
					return os.Remove(path)
				default:
					raw, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					var address sessionmove.SuccessorAddress
					if err := json.Unmarshal(raw, &address); err != nil {
						return err
					}
					ssh := *address.Route.SSH
					ssh.Host = "changed-target"
					address.Route.SSH = &ssh
					for path, value := range map[string]any{path: address, filepath.Join(fixture.store.Root, fixture.request.HandoffID, "route.json"): address.Route} {
						raw, err := json.MarshalIndent(value, "", "  ")
						if err != nil {
							return err
						}
						if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
							return err
						}
					}
					return nil
				}
			}
			_, err := Acknowledge(context.Background(), options)
			if err == nil {
				t.Fatal("uncorroborated address accepted")
			}
			if scenario == "publication hook fails" && !errors.Is(err, failure) {
				t.Fatalf("hook failure = %v", err)
			}
			if scenario == "address disappears" && !strings.Contains(err.Error(), "corroborate immutable successor address") {
				t.Fatalf("corroboration failure = %v", err)
			}
			if scenario == "route and address change" && (!errors.Is(err, sessionmove.ErrHandoffConflict) || !strings.Contains(err.Error(), "address changed")) {
				t.Fatalf("changed address = %v", err)
			}
		})
	}
}
