package defaultbranch

import (
	"context"
	"fmt"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

// fakeGit answers the Git port from in-memory tables and call scripts, the
// way internal/streams/fakes_test.go's fakeGit does for the streams ports.
// It is not yet wired into Run (that is the next lane's work, ports.go's
// doc comment explains why); it exists so that lane does not have to design
// the fake shape from scratch.
type fakeGit struct {
	runFunc              func(ctx context.Context, dir string, args ...string) (string, error)
	isAncestorFunc       func(ctx context.Context, dir, ancestor, descendant string) (bool, error)
	atomicRenameRefsFunc func(ctx context.Context, dir, source, destination, expected string) error
	attachHeadFunc       func(ctx context.Context, dir, destination string) error
	refExistsFunc        func(ctx context.Context, dir, ref string) (bool, error)

	// calls records every method invoked, in order, so a test can assert
	// call counts and ordering the way task-8's fail-call-N sweep will.
	calls []string
}

var _ Git = (*fakeGit)(nil)

func (f *fakeGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	f.calls = append(f.calls, "Run")
	if f.runFunc == nil {
		return "", fmt.Errorf("fakeGit.Run: no runFunc configured for %v", args)
	}
	return f.runFunc(ctx, dir, args...)
}

func (f *fakeGit) IsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
	f.calls = append(f.calls, "IsAncestor")
	if f.isAncestorFunc == nil {
		return false, fmt.Errorf("fakeGit.IsAncestor: no isAncestorFunc configured")
	}
	return f.isAncestorFunc(ctx, dir, ancestor, descendant)
}

func (f *fakeGit) AtomicRenameRefs(ctx context.Context, dir, source, destination, expected string) error {
	f.calls = append(f.calls, "AtomicRenameRefs")
	if f.atomicRenameRefsFunc == nil {
		return fmt.Errorf("fakeGit.AtomicRenameRefs: no atomicRenameRefsFunc configured")
	}
	return f.atomicRenameRefsFunc(ctx, dir, source, destination, expected)
}

func (f *fakeGit) AttachHead(ctx context.Context, dir, destination string) error {
	f.calls = append(f.calls, "AttachHead")
	if f.attachHeadFunc == nil {
		return fmt.Errorf("fakeGit.AttachHead: no attachHeadFunc configured")
	}
	return f.attachHeadFunc(ctx, dir, destination)
}

func (f *fakeGit) RefExists(ctx context.Context, dir, ref string) (bool, error) {
	f.calls = append(f.calls, "RefExists")
	if f.refExistsFunc == nil {
		return false, fmt.Errorf("fakeGit.RefExists: no refExistsFunc configured")
	}
	return f.refExistsFunc(ctx, dir, ref)
}

// fakeGitHub answers the GitHub port from in-memory tables.
type fakeGitHub struct {
	readFunc    func(ctx context.Context, endpoint string) ([]byte, error)
	executeFunc func(ctx context.Context, args ...string) githubobserver.CommandResponse

	calls []string
}

var _ GitHub = (*fakeGitHub)(nil)

func (f *fakeGitHub) Read(ctx context.Context, endpoint string) ([]byte, error) {
	f.calls = append(f.calls, "Read "+endpoint)
	if f.readFunc == nil {
		return nil, fmt.Errorf("fakeGitHub.Read: no readFunc configured for %s", endpoint)
	}
	return f.readFunc(ctx, endpoint)
}

func (f *fakeGitHub) Execute(ctx context.Context, args ...string) githubobserver.CommandResponse {
	f.calls = append(f.calls, "Execute")
	if f.executeFunc == nil {
		return githubobserver.CommandResponse{Err: fmt.Errorf("fakeGitHub.Execute: no executeFunc configured for %v", args)}
	}
	return f.executeFunc(ctx, args...)
}

// fakeDiscovery answers the Discovery port from in-memory tables, or from
// listRemoteFunc when a test needs ListRemote's behaviour to vary by call
// (an owner-keyed table cannot express, for example, "fail every call for
// this owner" versus "fail only this many calls").
type fakeDiscovery struct {
	authUser       string
	authUserErr    error
	memberOrgs     []string
	memberOrgsErr  error
	remoteRepos    map[string][]discover.Repo
	remoteErr      map[string]error
	listRemoteFunc func(owner string) ([]discover.Repo, error)
}

var _ Discovery = (*fakeDiscovery)(nil)

func (f *fakeDiscovery) AuthUser() (string, error) {
	return f.authUser, f.authUserErr
}

func (f *fakeDiscovery) MemberOrgs() ([]string, error) {
	return f.memberOrgs, f.memberOrgsErr
}

func (f *fakeDiscovery) ListRemote(owner string) ([]discover.Repo, error) {
	if f.listRemoteFunc != nil {
		return f.listRemoteFunc(owner)
	}
	if err := f.remoteErr[owner]; err != nil {
		return nil, err
	}
	return f.remoteRepos[owner], nil
}

// fakeClock answers the Clock port without a real sleep: Wait advances the
// fake's own notion of time instead of blocking, so a test that exercises a
// bounded-wait loop runs at test speed and deterministically, per the
// coverage-to-100 lane rules (the test picks paths, never a clock).
// nowFunc and waitFunc, when set, override the deterministic default —
// a test that must assert an exact call count or a specific injected
// duration sequence scripts them directly instead.
type fakeClock struct {
	now      time.Time
	waited   []time.Duration
	nowFunc  func() time.Time
	waitFunc func(ctx context.Context, d time.Duration) error
}

var _ Clock = (*fakeClock)(nil)

func (f *fakeClock) Now() time.Time {
	if f.nowFunc != nil {
		return f.nowFunc()
	}
	return f.now
}

func (f *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	if f.waitFunc != nil {
		return f.waitFunc(ctx, d)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.waited = append(f.waited, d)
	f.now = f.now.Add(d)
	return nil
}
