package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/agentmessage"
	"github.com/sneat-dev/wb/internal/agentmessagewake"
	"github.com/spf13/cobra"
)

func TestMessageWakeWiringUsesSelectedProjectsRootAndRejectsUnavailableTargets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	service, err := messageWakeService(root)
	if err != nil || service.Root != filepath.Join(root, ".wb", "messages", "wake") || service.ProjectsRoot != root {
		t.Fatalf("wrong local wake store: %+v, %v", service, err)
	}
	message := agentmessage.Message{Version: 1, ID: agentmessage.NewID(), From: "vm", To: "mac", Body: "test"}
	status, err := newMessageNotifier(&invocation{projectsRoot: root})(context.Background(), message)
	if err != nil || status != agentmessagewake.NotBound {
		t.Fatalf("unbound inbox: %s, %v", status, err)
	}
	deps := newMessageBindingDependencies()
	if err := deps.Bind(context.Background(), root, "mac", "relative.sock", "thread"); err == nil {
		t.Fatal("relative socket accepted")
	}
	if err := deps.Unbind(root, "mac"); err != nil {
		t.Fatalf("unbind absent: %v", err)
	}
	for _, command := range []*cobra.Command{newMessageBindCmd(&invocation{projectsRoot: root}), newMessageUnbindCmd(&invocation{projectsRoot: root})} {
		if command.Flags().Lookup("agent") == nil {
			t.Fatal("missing receiver selector")
		}
	}
}

func TestMessageWakeWiringPropagatesRootResolutionFailures(t *testing.T) {
	t.Parallel()
	badRoot := filepath.Join(t.TempDir(), "invalid\x00root")
	if _, err := messageWakeService(badRoot); err == nil {
		t.Fatal("invalid path accepted")
	}
	status, err := newMessageNotifier(&invocation{projectsRoot: badRoot})(context.Background(), agentmessage.Message{})
	if err == nil || status != agentmessagewake.PreflightFailed {
		t.Fatalf("root error lost: %s %v", status, err)
	}
	deps := newMessageBindingDependencies()
	if err := deps.Bind(context.Background(), badRoot, "mac", "/socket", "thread"); err == nil {
		t.Fatal("bind ignored invalid root")
	}
	if err := deps.Unbind(badRoot, "mac"); err == nil {
		t.Fatal("unbind ignored invalid root")
	}
}
