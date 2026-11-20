//go:build e2e

package dependencycache

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestE2EArchivePolicyFilesystemAndInheritedLeaseRegressions(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	output, err := exec.CommandContext(ctx, "python3", "cache_test.py").CombinedOutput()
	if err != nil {
		t.Fatalf("archive policy regressions: %v\n%s", err, output)
	}
}
