package landingcontext

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestCwCovLifecycleCheckoutUpdatedWarnsInsteadOfFailing(t *testing.T) {
	// A checkout that is not a repository has no identity; the hook dispatch
	// must warn and return rather than fail the caller's update. The
	// dispatch func is a fake: lifecycleCheckoutUpdated must never reach the
	// real lifecyclehooks.Dispatch (and so never the real config/state/
	// receipt paths or the real detached-worker launcher) from a test
	// binary (#620).
	var out bytes.Buffer
	var dispatchCalls int
	dispatch := func(context.Context, []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
		dispatchCalls++
		return lifecyclehooks.Report{}, nil
	}
	handler := CheckoutUpdated(&out, dispatch)
	handler(t.Context(), orchestrate.CheckoutUpdate{Checkout: filepath.Join(t.TempDir(), "absent")})
	if !strings.Contains(out.String(), "lifecycle hooks were not dispatched") {
		t.Fatalf("missing-identity warning = %q", out.String())
	}
	if dispatchCalls != 0 {
		t.Fatalf("dispatch calls = %d, want 0: an unidentifiable checkout must return before dispatching", dispatchCalls)
	}

	// A real repository is identified and the dispatch runs; with no hooks
	// configured there is nothing to warn about.
	repo := t.TempDir()
	testenv.Git(t, repo, "init", "-b", "main")
	testenv.Git(t, repo, "config", "user.email", "wb@example.test")
	testenv.Git(t, repo, "config", "user.name", "WB Test")
	testenv.Git(t, repo, "remote", "add", "origin", "https://github.com/acme/app.git")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testenv.Git(t, repo, "add", ".")
	testenv.Git(t, repo, "commit", "-m", "init")
	identity, identityErr := lifecyclehooks.RepositoryIdentity(repo)
	if identityErr != nil || identity != "github.com/acme/app" {
		t.Fatalf("fixture identity = (%q, %v), want github.com/acme/app", identity, identityErr)
	}
	out.Reset()
	handler(t.Context(), orchestrate.CheckoutUpdate{
		Checkout: repo, OldSHA: "old", NewSHA: "new", Cause: "test",
	})
	if strings.Contains(out.String(), "identify updated checkout") {
		t.Fatalf("a real repository should be identified:\n%s", out.String())
	}
	if dispatchCalls != 1 {
		t.Fatalf("dispatch calls = %d, want 1: an identified checkout must dispatch exactly once", dispatchCalls)
	}
	sentinel := errors.New("dispatch failed")
	out.Reset()
	warn := CheckoutUpdated(&out, func(ctx context.Context, events []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
		if ctx != t.Context() || len(events) != 1 || events[0] != (lifecyclehooks.Event{Name: lifecyclehooks.EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repo, OldSHA: "old", NewSHA: "new", Cause: "test"}) {
			t.Fatal(ctx, events)
		}
		return lifecyclehooks.Report{Warnings: []string{"warning receipt"}}, sentinel
	})
	warn(t.Context(), orchestrate.CheckoutUpdate{Checkout: repo, OldSHA: "old", NewSHA: "new", Cause: "test"})
	if !strings.Contains(out.String(), "warning: warning receipt") || !strings.Contains(out.String(), "warning: lifecycle hooks were not dispatched: dispatch failed") {
		t.Fatal(out.String())
	}
	// Actual default dispatcher sees this private repository with isolated empty configuration.
	out.Reset()
	CheckoutUpdated(&out, lifecyclehooks.Dispatch)(t.Context(), orchestrate.CheckoutUpdate{Checkout: repo, NewSHA: "new", Cause: "test"})
	if out.Len() != 0 {
		t.Fatal(out.String())
	}

}

func TestLifecycleCheckoutUpdatedDefaultsToRealDispatch(t *testing.T) {
	if handler := CheckoutUpdated(io.Discard, lifecyclehooks.Dispatch); handler == nil {
		t.Fatal("lifecycleCheckoutUpdated returned a nil handler")
	}
}
