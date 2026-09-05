package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func fakeCodeGrapherDeps(goos string, binaries map[string]string, responses map[string]string) codeGrapherDeps {
	return codeGrapherDeps{
		goos: goos,
		lookPath: func(name string) (string, error) {
			if path, ok := binaries[name]; ok {
				return path, nil
			}
			return "", errors.New("missing " + name)
		},
		evalSymlinks: func(path string) (string, error) { return path, nil },
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			key := name + " " + strings.Join(args, " ")
			if output, ok := responses[key]; ok {
				return []byte(output), nil
			}
			return nil, nil
		},
	}
}

func TestCodeGrapherStatusReportsInstalledBinaryProvenance(t *testing.T) {
	t.Parallel()
	deps := fakeCodeGrapherDeps("darwin", map[string]string{codeGrapherBinary: "/opt/homebrew/Caskroom/codegrapher/0.2.2/codegrapher"}, map[string]string{
		"/opt/homebrew/Caskroom/codegrapher/0.2.2/codegrapher version": "codegrapher 0.2.2 (abc123) 2026-09-05T00:00:00Z\n",
	})
	status := inspectCodeGrapher(context.Background(), deps)
	if !status.Installed || !status.Runnable || status.Version != "0.2.2" || status.Commit != "abc123" || status.Manager != "homebrew" {
		t.Fatalf("status = %+v", status)
	}
	if status.Repository != codeGrapherRepository || status.Module != codeGrapherGoModule {
		t.Fatalf("provenance = %+v", status)
	}
}

func TestCodeGrapherStatusAcceptsLegacyBareVersion(t *testing.T) {
	t.Parallel()
	deps := fakeCodeGrapherDeps("darwin", map[string]string{codeGrapherBinary: "/Users/alex/.local/bin/codegrapher"}, map[string]string{
		"/Users/alex/.local/bin/codegrapher version": "0.1.0\n",
	})
	status := inspectCodeGrapher(context.Background(), deps)
	if !status.Runnable || status.Version != "0.1.0" || status.Manager != "manual" {
		t.Fatalf("status = %+v", status)
	}
}

func TestCodeGrapherStatusMissingIsMachineReadableFinding(t *testing.T) {
	t.Parallel()
	command := newCodeGrapherStatusCmd(fakeCodeGrapherDeps("linux", nil, nil))
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--format=json"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "not installed") || !strings.Contains(out.String(), `"installed":false`) {
		t.Fatalf("err=%v output=%s", err, out.String())
	}
}

func TestCodeGrapherInstallRequiresExplicitApproval(t *testing.T) {
	t.Parallel()
	command := newCodeGrapherInstallCmd(fakeCodeGrapherDeps("darwin", map[string]string{"brew": "/opt/homebrew/bin/brew"}, nil), "install")
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "without --yes") {
		t.Fatalf("error = %v", err)
	}
}

func TestCodeGrapherDryRunUsesPlatformSpecificCommands(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, goos, verb, version, want string
	}{
		{"darwin install", "darwin", "install", "latest", "brew update && brew install --cask code-grapher/tap/codegrapher"},
		{"linux update", "linux", "update", "latest", "brew update && brew upgrade --cask codegrapher"},
		{"windows exact", "windows", "install", "0.2.2", "go install github.com/specscore/codegrapher/cmd/codegrapher@0.2.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binaries := map[string]string{"brew": "/brew", "go": "C:/go/bin/go.exe"}
			result, err := runCodeGrapherInstall(context.Background(), fakeCodeGrapherDeps(tc.goos, binaries, nil), tc.verb, tc.version, true)
			var commands []string
			for _, command := range result.Commands {
				commands = append(commands, strings.Join(command, " "))
			}
			if err != nil || strings.Join(commands, " && ") != tc.want {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestCodeGrapherRejectsHomebrewVersionPinAndUnknownPlatform(t *testing.T) {
	t.Parallel()
	deps := fakeCodeGrapherDeps("darwin", map[string]string{"brew": "/brew"}, nil)
	if _, err := runCodeGrapherInstall(context.Background(), deps, "install", "0.2.2", true); err == nil || !strings.Contains(err.Error(), "--version") {
		t.Fatalf("pin error = %v", err)
	}
	if _, err := runCodeGrapherInstall(context.Background(), fakeCodeGrapherDeps("plan9", nil, nil), "install", "latest", true); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("platform error = %v", err)
	}
}

func TestCodeGrapherJSONShortcutAndFormatConflict(t *testing.T) {
	t.Parallel()
	if got, err := codeGrapherFormat("text", true); err != nil || got != "json" {
		t.Fatalf("format = %q, %v", got, err)
	}
	if _, err := codeGrapherFormat("yaml", true); err == nil {
		t.Fatal("expected conflict")
	}
}

func TestCodeGrapherIsTheDefaultTypedPlugin(t *testing.T) {
	t.Parallel()
	command := newPluginListCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--format=json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{`"id":"codegrapher"`, `"default_enabled":true`, `"lifecycle":["status","install","update"]`} {
		if !strings.Contains(got, want) {
			t.Errorf("plugin output missing %s: %s", want, got)
		}
	}
}
