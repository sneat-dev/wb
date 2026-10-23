package sessionparkreceive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReceiveRefusesEvidenceLostAfterExecutionLock(t *testing.T) {
	t.Parallel()
	for _, evidence := range []string{"events", "receipt"} {
		t.Run(evidence, func(t *testing.T) {
			t.Parallel()
			fixture := newReceiveFixture(t, 1)
			options := fixture.options()
			options.afterExecutionLock = func() {
				aggregate := filepath.Join(options.Store.Root, fixture.request.ResumeID)
				if evidence == "events" {
					if err := os.RemoveAll(filepath.Join(aggregate, "events")); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(filepath.Join(aggregate, "receipt.json"), []byte("{invalid receipt"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := Receive(context.Background(), options); err == nil {
				t.Fatalf("accepted invalid %s evidence", evidence)
			}
			if fixture.received.Load() != 0 || fixture.prepared.Load() != 0 || fixture.completed.Load() != 0 {
				t.Fatalf("invalid %s allowed target mutation", evidence)
			}
		})
	}
}
