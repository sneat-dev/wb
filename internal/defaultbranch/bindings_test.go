package defaultbranch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

//nolint:paralleltest // This native fixture or its helper changes process-wide HOME, PATH or supervisor environment; testing restores it.
func TestDefaultBindingsUseActualObserverAndPrivateExecutable(t *testing.T) {
	bin := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '{}\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	service := New()
	raw, err := service.deps.Read(t.Context(), "repos/acme/app")
	if err != nil || string(raw) != "{}\n" {
		t.Fatal(string(raw), err)
	}
	response := service.deps.Execute(t.Context(), "api", "--method", "GET", "repos/acme/app")
	if response.Err != nil || string(response.Stdout) != "{}\n" {
		t.Fatal(response)
	}
	if err := service.deps.RenameWait(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := service.deps.RenameWait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
