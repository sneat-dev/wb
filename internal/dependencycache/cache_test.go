//go:build linux

package dependencycache

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestOwnedHelperHelpAndCancellationPreserveExit(t *testing.T) {
	t.Parallel()
	var out, diagnostic bytes.Buffer
	if err := Run(context.Background(), []string{"--help"}, &out, &diagnostic); err != nil {
		t.Fatalf("help: %v; %s", err, diagnostic.String())
	}
	if !strings.Contains(out.String(), "--source-manifest") || !strings.Contains(out.String(), "--apply") {
		t.Fatalf("missing owned-helper options: %s", out.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, []string{"--help"}, &out, &diagnostic); err == nil {
		t.Fatal("cancelled helper reported success")
	}
}
