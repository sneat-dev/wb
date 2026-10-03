package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentRootAwaitAliasAndLazyLocalStoreBinding(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := filepath.Join(root, ".wb")
	store := agents.NewStore(home)
	id, err := agents.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(agents.Record{AgentID: id, State: agents.StateCompleted}); err != nil {
		t.Fatal(err)
	}
	inv := testInvocation(t, "before-parse")
	command := newWaitAgentCmd(inv)
	if command.Name() != "agent" || !strings.Contains(strings.Join(command.Aliases, " "), "await") {
		t.Fatal(command.Use, command.Aliases)
	}
	inv.projectsRoot = root
	var out, errout bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errout)
	command.SetArgs([]string{id, "--json"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err, errout.String())
	}
	var result agents.Result
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.AgentID != id || !result.Terminal {
		t.Fatal(result, err)
	}
}

func TestAgentPrivateRootEntryEstablishesDefaultsAndReturnsStructuredRefusals(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", root)
	inv := testInvocation(t, "")
	var out, errout bytes.Buffer
	if code := RunAgentRemote(inv, strings.NewReader(`{"schema_version":1,"operation":"list"}`), &out, &errout); code != 0 || inv.projectsRoot != root || !strings.Contains(out.String(), `"results": []`) {
		t.Fatal(code, inv.projectsRoot, out.String(), errout.String())
	}
	out.Reset()
	if code := RunAgentRemote(testInvocation(t, root), strings.NewReader("malformed"), &out, &errout); code != 0 || !strings.Contains(out.String(), "protocol document") {
		t.Fatal(code, out.String())
	}
}

func TestAgentRootRemoteBindingsUseRealConfigurationAndPrivateSSHStdin(t *testing.T) {
	root := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Dir(wbconfig.DefaultPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wbconfig.DefaultPath(), []byte("session_move:\n  targets:\n    private:\n      default_courier: ssh\n      ssh:\n        host: fixture.invalid\n        user: fixture\n        wb_path: /private/wb\n"), 0600); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	requestPath := filepath.Join(directory, "request")
	ssh := filepath.Join(directory, "ssh")
	script := "#!/bin/sh\ncat > " + requestPath + "\nprintf '%s\\n' '{\"schema_version\":1,\"operation\":\"status\",\"result\":{\"agent_id\":\"agt-00000000000000000000000000000000\",\"state\":\"completed\"}}'\n"
	if err := testenv.WriteExecutableFile(ssh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	inv := testInvocation(t, root)
	command := newAgentCmd(inv)
	var out, errout bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errout)
	command.SetArgs([]string{"status", "--to=private", "agt-00000000000000000000000000000000", "--json"})
	if err := command.ExecuteContext(context.Background()); err != nil || !strings.Contains(out.String(), `"machine": "private"`) {
		t.Fatal(err, out.String(), errout.String())
	}
	raw, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	var request agents.RemoteRequest
	if err := json.Unmarshal(raw, &request); err != nil || request.Operation != "status" {
		t.Fatal(request, err)
	}
}
