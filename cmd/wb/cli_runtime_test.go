package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
)

func TestCLIRuntimeReadsAllFlagsAfterRootParsing(t *testing.T) {
	t.Parallel()
	inv := &invocation{}
	runtime := newCLIRuntime(inv)
	root := newRootCmdFor(inv)
	if err := root.ParseFlags([]string{"--projects-root=/parsed", "--filter=acme", "--org=one", "--org=two", "--non-interactive", "--quiet"}); err != nil {
		t.Fatal(err)
	}
	want := shared.Flags{ProjectsRoot: "/parsed", Filter: "acme", ExtraOrgs: []string{"one", "two"}, NonInteractive: true, Quiet: true}
	if got := runtime.Flags(); !reflect.DeepEqual(got, want) {
		t.Fatalf("flags=%+v want %+v", got, want)
	}
	// A consumer cannot change the invocation's repeatable flag through its snapshot.
	snapshot := runtime.Flags()
	snapshot.ExtraOrgs[0] = "changed"
	if runtime.Flags().ExtraOrgs[0] != "one" {
		t.Fatal("flag snapshot mutated invocation")
	}
}

func TestCLIRuntimeInstancesKeepSeparateFlags(t *testing.T) {
	t.Parallel()
	first := &invocation{projectsRoot: "first", filterFlag: "a", extraOrgs: []string{"one"}, nonInteractive: true}
	second := &invocation{projectsRoot: "second", filterFlag: "b", extraOrgs: []string{"two"}, quiet: true}
	firstRuntime, secondRuntime := newCLIRuntime(first), newCLIRuntime(second)
	first.projectsRoot = "updated"
	if got := firstRuntime.Flags(); got.ProjectsRoot != "updated" || got.Filter != "a" || got.Quiet || !got.NonInteractive {
		t.Fatalf("first=%+v", got)
	}
	if got := secondRuntime.Flags(); got.ProjectsRoot != "second" || got.Filter != "b" || !got.Quiet || got.NonInteractive || got.ExtraOrgs[0] != "two" {
		t.Fatalf("second=%+v", got)
	}
}

func TestCLIRuntimePreservesRootCodedErrorIdentity(t *testing.T) {
	t.Parallel()
	runtime := newCLIRuntime(&invocation{})
	for _, code := range []int{exitFindings, exitUsage} {
		err := runtime.ExitError(code, "specific diagnostic")
		var coded *exitError
		if !errors.As(err, &coded) || coded.code != code || err.Error() != "specific diagnostic" || exitCodeFor(err, false) != code {
			t.Fatalf("code=%d error=%v", code, err)
		}
	}
}

func TestCLIRuntimeOutputFormatBridgePreservesDiagnostic(t *testing.T) {
	t.Parallel()
	if err := requireOutputFormat("json", "text", "json"); err != nil {
		t.Fatal(err)
	}
	if err := requireOutputFormat("yaml", "text", "json"); err == nil || err.Error() != `unsupported format "yaml"; use text or json` {
		t.Fatalf("error=%v", err)
	}
}
