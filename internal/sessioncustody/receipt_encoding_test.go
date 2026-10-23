package sessioncustody

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestReplayRejectsReceiptsOutsideJSONTimezoneRange(t *testing.T) {
	t.Parallel()
	for _, durable := range []bool{false, true} {
		name := "supplied receipt"
		if durable {
			name = "durable receipt"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newCustodyFixture(t)
			options := fixture.options
			options.SealWorkLog = successfulSeal(fixture)
			if _, err := Acknowledge(context.Background(), options); err != nil {
				t.Fatal(err)
			}
			invalid := fixture.receipt.StartedAt.In(time.FixedZone("out-of-range", 24*60*60))
			if durable {
				path := filepath.Join(fixture.store.Root, fixture.request.HandoffID, "receipt.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				raw = []byte(strings.Replace(string(raw), fixture.receipt.StartedAt.Format(time.RFC3339), invalid.Format(time.RFC3339), 1))
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				options.Receipt.StartedAt = invalid
			}
			options.SealWorkLog = func(_ worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
				t.Fatal("invalid receipt reached source seal")
				return worktrees.ExternalSourceSealResult{}, nil
			}
			if _, err := Acknowledge(context.Background(), options); err == nil || !strings.Contains(err.Error(), "timezone hour outside of range") {
				t.Fatalf("invalid %s encoding = %v", name, err)
			}
		})
	}
}
