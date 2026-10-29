//go:build e2e && linux

package agents

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// TestContainerRuntimeDefaultCommandWrapperIsBuiltBeforeTheDaemonIsRead
// exercises the production command wrapper the runtime builds when no test
// command seam is supplied: it starts the harmless docker stand-in for real,
// so it belongs to the e2e tier. The stand-in answers the daemon identity probe
// with an empty ID, which the runtime must refuse before it touches the slot.
func TestE2EContainerRuntimeDefaultCommandWrapperIsBuiltBeforeTheDaemonIsRead(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	script := filepath.Join(t.TempDir(), "docker")
	if err := testenv.WriteExecutableFile(script, []byte("#!/bin/sh\ncase \"$1\" in context) printf 'default\\n';; info) printf '';; *) printf 'unused\\n';; esac\n"), 0o700); err != nil {
		t.Fatalf("write the harmless docker stand-in: %v", err)
	}
	fake := newReadyFake(t, policy)
	options := testContainerRuntimeOptions(testContainerSlotStateDir(t), fake)
	options.commands = nil
	options.host.dockerPath = func() (string, error) { return script, nil }
	options.host.lookupEnv = func(string) (string, bool) { return "", false }
	runtime, err := OpenContainerRuntime(context.Background(), options)
	if err == nil {
		_ = runtime.Close()
		t.Fatal("an invalid daemon identity must refuse the runtime")
	}
	if !strings.Contains(err.Error(), "bounded Docker Engine identity") {
		t.Fatalf("error = %v, want the invalid daemon identity named", err)
	}
}
