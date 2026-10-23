package main

import "testing"

func TestSyncReportRootRegistryPreservesDiscoveryAnnotations(t *testing.T) {
	t.Parallel()
	inv := testInvocation(t, t.TempDir())
	root := newRootCmdFor(inv)
	command, args, err := root.Find([]string{"sync-report"})
	if err != nil || len(args) != 0 || command.Name() != "sync-report" {
		t.Fatalf("command=%v args=%v err=%v", command, args, err)
	}
	terms := map[string]string{
		"sync-report": "sync report analysis findings repository ingitdb markdown persist publish web view",
		"validate":    "sync report validate check agent markdown ingitdb schema records",
		"publish":     "sync report publish persist commit push workbench repository ingitdb URL web view",
	}
	if command.Annotations[discoveryTermsAnnotation] != terms["sync-report"] || len(command.Commands()) != 2 {
		t.Fatalf("family=%v children=%v", command.Annotations, command.Commands())
	}
	for _, child := range command.Commands() {
		if child.Hidden || child.Annotations[discoveryTermsAnnotation] != terms[child.Name()] {
			t.Fatalf("child=%s annotations=%v hidden=%t", child.Name(), child.Annotations, child.Hidden)
		}
	}
}
