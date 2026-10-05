package mergepolicy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

//nolint:paralleltest // This contract changes process-wide PATH/HOME/WB_HOME; environment-mutating rows remain serial.
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
	repos, err := service.deps.Discover("projects", "", nil, []string{"acme/app"}, false)
	if err != nil || len(repos) != 1 || !repos[0].Remote {
		t.Fatal(repos, err)
	}
}
