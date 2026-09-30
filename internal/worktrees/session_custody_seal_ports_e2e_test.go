package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

//nolint:paralleltest // source fixtures set process environment for isolated real-Git repositories
func TestE2EExternalSourceSealRetainsDurableReceiptAuthority(t *testing.T) {
	fixture := newExternalSourceFixture(t)
	lock := fixture.lock(t)
	options := ExternalSourceSealOptions{Store: fixture.store, ExecutionLock: lock,
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request,
		RequestDigest: fixture.digest, Receipt: fixture.receipt(t), SourceSession: fixture.source}
	invalid := options
	invalid.Receipt.TargetMachine = "unrelated"
	if _, err := SealExternalSessionWorkLog(invalid); err == nil || !strings.Contains(err.Error(), "receipt") {
		t.Fatalf("unrelated receipt error = %v", err)
	}
	missingLock := options
	missingLock.ExecutionLock = nil
	if _, err := SealExternalSessionWorkLog(missingLock); err == nil || !strings.Contains(err.Error(), "retained durable receipt") {
		t.Fatalf("missing lock error = %v", err)
	}
	wrongStore := options
	wrongStore.Store = sessionmove.Store{}
	if _, err := SealExternalSessionWorkLog(wrongStore); err == nil || !strings.Contains(err.Error(), "load durable source receipt") {
		t.Fatalf("wrong store error = %v", err)
	}
	if _, err := SealExternalSessionWorkLog(options); err == nil || !strings.Contains(err.Error(), "receipt does not exactly authorize") {
		t.Fatalf("unpublished receipt error = %v", err)
	}
	options.Receipt = fixture.authorizeSeal(t, lock)
	wrongSession := options
	wrongSession.SourceSession.Model = "unrelated-model"
	if _, err := SealExternalSessionWorkLog(wrongSession); err == nil || !strings.Contains(err.Error(), "source session") {
		t.Fatalf("unrelated predecessor error = %v", err)
	}
	loop := filepath.Join(t.TempDir(), "root-loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	invalidHome := options
	invalidHome.ProjectsRoot = loop
	if _, err := SealExternalSessionWorkLog(invalidHome); err == nil {
		t.Fatal("cyclic projects root resolved as a valid custody home")
	}
}

//nolint:paralleltest // each source fixture uses t.Setenv while creating an isolated real-Git repository
func TestE2EExternalSourceSealStopsAtFailedWorkLogStage(t *testing.T) {
	failure := errors.New("injected custody stage failure")
	cases := []struct {
		name   string
		change func(*externalSourceFixture, *externalSourceSealPorts)
		want   string
	}{
		{"open run", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.openRun = func(string, string, string, string, bool) (*lockedWorkLogRun, error) { return nil, failure }
		}, "injected custody stage failure"},
		{"read claim", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.readClaim = func(*os.File, string) (workLogClaim, error) { return workLogClaim{}, failure }
		}, "injected custody stage failure"},
		{"read projection", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.readProjection = func(string) (workLogProjection, error) { return workLogProjection{}, failure }
		}, "injected custody stage failure"},
		{"projection lineage", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.readProjection = func(string) (workLogProjection, error) { return workLogProjection{}, nil }
		}, "projection conflicts"},
		{"source offer", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.validateOffer = func(string, sessionmove.Request, sessionmove.Digest) error { return failure }
		}, "injected custody stage failure"},
		{"existing terminal", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.validateTerminal = func(*os.File, workLogClaim, sessionmove.Request, sessionmove.WorkLogReference, *workLogExternalHandoffEvidence) (bool, time.Time, error) {
				return false, time.Time{}, failure
			}
		}, "injected custody stage failure"},
		{"terminal projection without record", func(f *externalSourceFixture, p *externalSourceSealPorts) {
			p.readProjection = func(string) (workLogProjection, error) {
				return workLogProjection{EffortID: f.claim.EffortID, RunID: f.claim.RunID, ClaimID: f.claim.ClaimID, Lifecycle: "terminal"}, nil
			}
		}, "no exact immutable external terminal"},
		{"live owner", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.validateOwner = func(string, session.Record, sessionmove.Request, sessionmove.Digest, workLogClaim) error {
				return failure
			}
		}, "injected custody stage failure"},
		{"Git status", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.gitStatus = func(string) (string, error) { return "", failure }
		}, "inspect predecessor worktree"},
		{"dirty Git status", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.gitStatus = func(string) (string, error) { return " M README.md", nil }
		}, "changed after its handoff bundle"},
		{"claim corroboration", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.corroborate = func(string, string, workLogProjection, workLogClaim) error { return failure }
		}, "corroborate source Work Log"},
		{"terminal storage", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.sealTerminal = func(string, *os.File, worktreeclaims.TerminalSealRequest) (time.Time, error) {
				return time.Time{}, failure
			}
		}, "injected custody stage failure"},
		{"immutable retry time", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.validateTerminal = func(*os.File, workLogClaim, sessionmove.Request, sessionmove.WorkLogReference, *workLogExternalHandoffEvidence) (bool, time.Time, error) {
				return true, time.Unix(1, 0), nil
			}
			p.sealTerminal = func(string, *os.File, worktreeclaims.TerminalSealRequest) (time.Time, error) {
				return time.Unix(2, 0), nil
			}
		}, "changed its immutable sealed time"},
		{"projection publication", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.writeProjection = func(string, workLogProjection) error { return failure }
		}, "injected custody stage failure"},
		{"completion journal", func(_ *externalSourceFixture, p *externalSourceSealPorts) {
			p.appendEvent = func(string, LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
				return LocalWorkLogEvent{}, LocalWorkLogProjection{}, failure
			}
		}, "injected custody stage failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newExternalSourceFixture(t)
			lock := fixture.lock(t)
			receipt := fixture.authorizeSeal(t, lock)
			options := ExternalSourceSealOptions{Store: fixture.store, ExecutionLock: lock,
				ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request,
				RequestDigest: fixture.digest, Receipt: receipt, SourceSession: fixture.source}
			ports := defaultExternalSourceSealPorts()
			tc.change(fixture, &ports)
			if _, err := ports.sealExternalSessionWorkLog(options); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s error = %v, want %q", tc.name, err, tc.want)
			}
		})
	}
}
