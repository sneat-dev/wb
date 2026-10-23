package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/streams"
)

func TestDepsPropagationRootRegistryAndCurrentNativeStore(t *testing.T) {
	t.Parallel()
	first, current := t.TempDir(), t.TempDir()
	broken, err := streams.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	cwCovWriteFile(t, filepath.Join(broken.Root, "broken", "stream.json"), "invalid: [")
	inv := testInvocation(t, first)
	family := newDepsCmd(inv)
	command, _, err := family.Find([]string{"propagate"})
	if err != nil || command.Name() != "propagate" {
		t.Fatalf("registry=%v", err)
	}
	family.RemoveCommand(command)
	if len(command.Commands()) != 1 || command.Commands()[0].Name() != "local" || command.Annotations[discoveryTermsAnnotation] == "" || command.Commands()[0].Annotations[discoveryTermsAnnotation] == "" {
		t.Fatal("registry/discovery annotations changed")
	}
	inv.projectsRoot = current
	command.SilenceUsage = true
	command.SilenceErrors = true
	var firstOut, secondOut bytes.Buffer
	command.SetOut(&firstOut)
	command.SetArgs([]string{"local", "--undo", "--to", current, "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result locallink.Result
	if err := json.Unmarshal(firstOut.Bytes(), &result); err != nil || len(result.Plan) != 3 || len(result.Consumers) != 1 {
		t.Fatalf("actual undo=%s %v", firstOut.String(), err)
	}
	command.SetOut(&secondOut)
	command.SetArgs([]string{"local", "--undo", "--to", current, "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if secondOut.Len() == 0 || !json.Valid(secondOut.Bytes()) {
		t.Fatalf("repeat current output=%s", secondOut.String())
	}
}
