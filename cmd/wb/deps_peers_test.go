package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The exit code is the half an agent branches on, so it is pinned separately
// from the library's verdict logic: a blocked reuse must be exit 1 with the
// full table already on stdout, not an error that swallows the evidence.
func TestDepsPeersExitsWithFindingsAndPrintsTheVerdictTable(t *testing.T) {
	target := t.TempDir()
	writeDepsPeersFile(t, filepath.Join(target, "package.json"), `{
  "name": "@acme/host",
  "dependencies": {"react": "^18.0.0"}
}
`)
	writeDepsPeersFile(t, filepath.Join(target, "pnpm-lock.yaml"), `lockfileVersion: '9.0'

importers:
  .:
    dependencies:
      react:
        specifier: ^18.0.0
        version: 17.0.2
`)
	// A registry stub on PATH keeps the command hermetic while still exercising
	// the real pnpm invocation path, rather than bypassing it with an injected
	// resolver the shipped binary never uses.
	bin := t.TempDir()
	writeDepsPeersFile(t, filepath.Join(bin, "pnpm"), `#!/bin/sh
echo '{"version":"2.1.0","peerDependencies":{"react":"^18.0.0"}}'
`)
	if err := os.Chmod(filepath.Join(bin, "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out bytes.Buffer
	command := newDepsPeersCmd()
	command.SetArgs([]string{"@acme/widget", "--against", target})
	command.SetOut(&out)
	command.SetErr(&out)
	command.SilenceUsage = true

	err := command.Execute()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitFindings {
		t.Fatalf("error = %v, want an exitFindings error", err)
	}
	rendered := out.String()
	for _, want := range []string{"WB npm peer compatibility", "`react`", "unsatisfied", "17.0.2"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("output missing %q:\n%s", want, rendered)
		}
	}
}

func TestDepsPeersRejectsAnUnknownFormat(t *testing.T) {
	target := t.TempDir()
	writeDepsPeersFile(t, filepath.Join(target, "package.json"), `{"name":"@acme/host"}`)
	bin := t.TempDir()
	writeDepsPeersFile(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\necho '{\"version\":\"2.1.0\"}'\n")
	if err := os.Chmod(filepath.Join(bin, "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	command := newDepsPeersCmd()
	command.SetArgs([]string{"@acme/widget", "--against", target, "--format", "toml"})
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SilenceUsage = true
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unknown --format") {
		t.Fatalf("error = %v, want an unknown-format rejection", err)
	}
}

func writeDepsPeersFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
