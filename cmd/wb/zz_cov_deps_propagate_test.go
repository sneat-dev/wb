package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/streams"
)

func cwDepsPropagateResultFixture() locallink.Result {
	return locallink.Result{
		Library: "/tmp/cw/library", LibraryRepository: "acme/library", ContentHash: "abc123", Dirty: true, Stream: "cw-stream",
		Identities: []streams.Identity{{Ecosystem: streams.EcosystemGo, Name: "github.com/acme/library", Manifest: "go.mod"}},
		Plan:       []string{"link github.com/acme/library into acme/app", "verify acme/app"},
		Consumers: []locallink.ConsumerResult{
			{Consumer: "/tmp/cw/app", Skipped: true, Reason: "declares none of the library's identities"},
			{Consumer: "/tmp/cw/site",
				Links:         []streams.Link{{Identity: "github.com/acme/library", Mechanism: streams.MechanismGoWork, PreviousVersion: "v1.2.3"}},
				SkippedChecks: []string{"no pnpm lockfile to freeze"},
				Notes:         []string{"a published package already supersedes the record"},
				Verification: &locallink.Verification{
					Statement:   "verified against unpublished github.com/acme/library at content-hash abc123 (dirty)",
					ActiveLinks: []string{"github.com/acme/library was v1.2.3"},
					Linked: locallink.VerificationRun{Passed: false, Command: "go test -p 1 ./...",
						Details: []string{"TestThing failed"}},
					PublishedBaseline: locallink.VerificationRun{Passed: true, Command: "GOWORK=off go build ./...",
						Details: []string{"baseline note"}},
					Passed: true,
				}},
		},
	}
}

func TestCwDepsPrintPropagateLocalTextAndJSON(t *testing.T) {
	result := cwDepsPropagateResultFixture()
	var out bytes.Buffer
	if err := printPropagateLocal(cwDepsNewOutCommand(&out), "text", result); err != nil {
		t.Fatalf("printPropagateLocal text: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"plan:",
		"  - link github.com/acme/library into acme/app",
		"library /tmp/cw/library at content-hash abc123 (dirty)",
		"  publishes go github.com/acme/library (go.mod)",
		"/tmp/cw/app",
		"  skipped: declares none of the library's identities",
		"  linked github.com/acme/library via go.work (was v1.2.3)",
		"  ? not checked: no pnpm lockfile to freeze",
		"verified against unpublished github.com/acme/library",
		"active link: github.com/acme/library was v1.2.3",
		"linked run: passed=false go test -p 1 ./...",
		"! TestThing failed",
		"published baseline: passed=true GOWORK=off go build ./...",
		"! baseline note",
		"a published package already supersedes the record",
		"while these links are live, do not run",
		"--to /tmp/cw/site --undo",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("propagate text missing %q:\n%s", want, text)
		}
	}
	// The footer must not invite an --undo for a skipped consumer.
	if strings.Contains(text, "--to /tmp/cw/app ") || strings.Contains(text, "--to /tmp/cw/app\n") {
		t.Errorf("a skipped consumer appeared in the undo hint:\n%s", text)
	}
	// A clean library and a failed run are both named.
	out.Reset()
	clean := result
	clean.Dirty = false
	if err := printPropagateLocal(cwDepsNewOutCommand(&out), "text", result); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := printPropagateLocal(cwDepsNewOutCommand(&out), "text", clean); err == nil && !strings.Contains(out.String(), "(clean)") {
		t.Errorf("clean state not reported:\n%s", out.String())
	}
	// A per-consumer error is printed and suppresses the undo footer: an
	// undo hint on a run that did not link everything is a wrong instruction.
	out.Reset()
	failed := cwDepsPropagateResultFixture()
	failed.Consumers = []locallink.ConsumerResult{{Consumer: "/tmp/cw/app", Errors: []string{"go.work could not be written"}}}
	if err := printPropagateLocal(cwDepsNewOutCommand(&out), "text", failed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "! go.work could not be written") {
		t.Errorf("a consumer error was not printed:\n%s", out.String())
	}
	if strings.Contains(out.String(), "while these links are live") {
		t.Errorf("a failed run printed the undo footer:\n%s", out.String())
	}
	// JSON carries the structured result.
	out.Reset()
	if err := printPropagateLocal(cwDepsNewOutCommand(&out), "json", result); err != nil {
		t.Fatalf("printPropagateLocal json: %v", err)
	}
	var decoded locallink.Result
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("propagate JSON: %v\n%s", err, out.String())
	}
	if decoded.Library != result.Library || len(decoded.Consumers) != len(result.Consumers) {
		t.Fatalf("decoded = %+v", decoded)
	}
	// A failed write is surfaced.
	if err := printPropagateLocal(cwDepsNewOutCommand(cwDepsFailingWriter{}), "text", result); err == nil {
		t.Error("a failed text write must be surfaced")
	}
	if err := printPropagateLocal(cwDepsNewOutCommand(cwDepsFailingWriter{}), "json", result); err == nil {
		t.Error("a failed json write must be surfaced")
	}
}

func TestCwDepsConsumerPathsSkipsTheUnlinked(t *testing.T) {
	result := locallink.Result{Consumers: []locallink.ConsumerResult{
		{Consumer: "/tmp/a"},
		{Consumer: "/tmp/b", Skipped: true},
		{Consumer: "/tmp/c"},
	}}
	paths := consumerPaths(result)
	if len(paths) != 2 || paths[0] != "/tmp/a" || paths[1] != "/tmp/c" {
		t.Fatalf("consumerPaths = %v", paths)
	}
	if got := consumerPaths(locallink.Result{}); len(got) != 0 {
		t.Fatalf("consumerPaths of an empty result = %v", got)
	}
}
