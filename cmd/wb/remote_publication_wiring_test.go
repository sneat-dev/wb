package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

func TestRemotePublicationRootBindingsUseTheActualOwners(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("provider construction refused")
	deps := defaultRemoteDeps()
	deps.configPath = filepath.Join(t.TempDir(), "missing.yaml")
	deps.login = func() (string, error) { return "fixture-login", nil }
	deps.now = func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) }
	deps.open = func(remotestate.Config, string) (remotestate.Provider, error) { return nil, sentinel }
	bound := remotePublishDependencies(deps)
	if login, err := bound.Login(); err != nil || login != "fixture-login" || !bound.Now().Equal(deps.now()) || bound.ConfigPath != deps.configPath {
		t.Fatalf("current root publication dependencies=%+v %q %v", bound, login, err)
	}
	if _, err := bound.Open(remotestate.Config{}, t.TempDir()); !errors.Is(err, sentinel) {
		t.Fatalf("bound provider=%v", err)
	}
	_, _, err := loadRemote(deps, t.TempDir())
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitUsage {
		t.Fatalf("root config refusal=%T %v", err, err)
	}
	if _, err := openRemote(remotestate.Config{Provider: "ftp"}, t.TempDir()); !errors.As(err, &coded) || coded.code != exitUsage {
		t.Fatalf("root provider refusal=%T %v", err, err)
	}
	config := wbconfig.DefaultCockpitConfig()
	root, home := t.TempDir(), t.TempDir()
	options := cockpitFleetOptions(root, home, deps.configPath, config, io.Discard, func() (string, error) { return "root-machine", nil })
	if options.Machine != "root-machine" || options.ProjectsRoot != root || options.Version == "" || options.Collectors.Repositories == nil || options.Publisher != nil || options.PullRequests == nil {
		t.Fatalf("actual fleet binding=%+v", options)
	}
}

// This serial fixture uses the existing real private Git provider and scoped
// author environment; it exercises the root adapter's nil-progress fallback.
func TestRemotePublicationNilProgressRoutesNotesToCurrentStderr(t *testing.T) {
	fixture := newRemoteFixture(t, "root-notes-machine")
	deps := fixture.deps("root-notes-login", time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	var notes, stdout bytes.Buffer
	deps.stderr = &notes
	if err := runRemotePublishWithProgress(deps, fixture.projectsRoot, "", 2, false, true, &stdout, nil, &invocation{nonInteractive: true}); err != nil {
		t.Fatalf("real publication with nil progress: %v", err)
	}
	if !strings.Contains(notes.String(), "this publish also includes this machine's os, arch, cpu_count and boot_time") {
		t.Fatalf("current stderr did not receive hardware note: %q", notes.String())
	}
	if strings.Contains(stdout.String(), "this publish") || strings.Contains(stdout.String(), "agents and metrics") {
		t.Fatalf("notes contaminated JSON stdout: %q", stdout.String())
	}
	var report struct {
		Key                 string `json:"key"`
		RepositoriesScanned int    `json:"repositories_scanned"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || report.Key != "root-notes-login/root-notes-machine" || report.RepositoriesScanned != 1 {
		t.Fatalf("single actual report=%+v error=%v stdout=%q", report, err, stdout.String())
	}
}
