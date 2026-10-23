package worktrees

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

func transferClaimTestPorts(t *testing.T, order *[]string) transferClaimPorts {
	t.Helper()
	claim := workLogClaim{Version: 2, EffortID: "task", RunID: "run", ClaimID: strings.Repeat("a", 64),
		Task: "task", Repository: "acme/app", Worktree: "/checkout", Branch: "wb/task", Base: "main",
		BaseSHA: strings.Repeat("b", 40), Lifecycle: "active"}
	projection := workLogProjection{Version: 1, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, Lifecycle: "active"}
	return transferClaimPorts{
		readProjectionForClaim: func(string, string) (workLogProjection, error) { return projection, nil },
		openRun: func(string, string, string, string, bool) (*lockedWorkLogRun, error) {
			dir, err := os.Open(t.TempDir())
			return &lockedWorkLogRun{directory: dir, unlock: func() {}}, err
		},
		readProjection: func(string) (workLogProjection, error) { return projection, nil },
		openChild:      func(*os.File, string, bool) (*os.File, error) { return os.Open(t.TempDir()) },
		readJSON:       func(_ *os.File, _ string, value any) error { *value.(*workLogClaim) = claim; return nil },
		corroborate:    func(string, string, workLogProjection, workLogClaim) error { return nil },
		sealTerminal: func(_ string, _ *os.File, request worktreeclaims.TerminalSealRequest) (time.Time, error) {
			*order = append(*order, "terminal")
			if request.Claim.ClaimID != claim.ClaimID || request.SuccessorAgentID != "next" || request.FinalCommit != "commit" {
				t.Fatalf("terminal transfer request = %#v", request)
			}
			return time.Unix(42, 0).UTC(), nil
		},
		writeImmutable: func(_ *os.File, _ string, value any, idempotent bool) error {
			if !idempotent {
				t.Fatal("successor authority must be idempotent")
			}
			switch value.(type) {
			case workLogClaim:
				*order = append(*order, "successor claim")
			case workLogPublicEvent:
				*order = append(*order, "successor outbox")
			default:
				t.Fatalf("unexpected authority type %T", value)
			}
			return nil
		},
		openOutbox: func(string, string, bool) (*os.File, error) { return os.Open(t.TempDir()) },
		writeProjection: func(_ string, p workLogProjection) error {
			*order = append(*order, "projection")
			if p.Lifecycle != "active" || p.ClaimID == claim.ClaimID {
				t.Fatalf("successor projection = %#v", p)
			}
			return nil
		},
	}
}

func TestClaimTransferPublishesTerminalBeforeSuccessorAuthority(t *testing.T) {
	t.Parallel()
	var order []string
	ports := transferClaimTestPorts(t, &order)
	if err := ports.transferWorkLogClaim("/home", "/checkout", "commit", "handoff", "next", ClaimExecutionIdentity{Model: "unknown"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"terminal", "successor claim", "successor outbox", "projection"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("authority publication order = %q, want %q", order, want)
	}
}

func TestClaimTransferStopsAtEachFailedAuthorityStage(t *testing.T) {
	t.Parallel()
	failure := errors.New("injected transfer failure")
	cases := []struct {
		name   string
		change func(*transferClaimPorts)
		want   string
	}{
		{"read projection", func(p *transferClaimPorts) {
			p.readProjectionForClaim = func(string, string) (workLogProjection, error) { return workLogProjection{}, failure }
		}, "injected transfer failure"},
		{"open locked run", func(p *transferClaimPorts) {
			p.openRun = func(string, string, string, string, bool) (*lockedWorkLogRun, error) { return nil, failure }
		}, "injected transfer failure"},
		{"projection changes under lock", func(p *transferClaimPorts) {
			p.readProjection = func(string) (workLogProjection, error) { return workLogProjection{}, nil }
		}, "projection changed"},
		{"projection read fails under lock", func(p *transferClaimPorts) {
			p.readProjection = func(string) (workLogProjection, error) { return workLogProjection{}, failure }
		}, "projection changed"},
		{"open claims", func(p *transferClaimPorts) {
			p.openChild = func(*os.File, string, bool) (*os.File, error) { return nil, failure }
		}, "injected transfer failure"},
		{"read claim", func(p *transferClaimPorts) { p.readJSON = func(*os.File, string, any) error { return failure } }, "injected transfer failure"},
		{"corroborate claim", func(p *transferClaimPorts) {
			p.corroborate = func(string, string, workLogProjection, workLogClaim) error { return failure }
		}, "injected transfer failure"},
		{"seal terminal", func(p *transferClaimPorts) {
			p.sealTerminal = func(string, *os.File, worktreeclaims.TerminalSealRequest) (time.Time, error) {
				return time.Time{}, failure
			}
		}, "injected transfer failure"},
		{"write successor claim", func(p *transferClaimPorts) {
			p.writeImmutable = func(*os.File, string, any, bool) error { return failure }
		}, "write immutable successor claim"},
		{"open successor outbox", func(p *transferClaimPorts) {
			p.openOutbox = func(string, string, bool) (*os.File, error) { return nil, failure }
		}, "injected transfer failure"},
		{"write successor outbox", func(p *transferClaimPorts) {
			original := p.writeImmutable
			p.writeImmutable = func(dir *os.File, name string, value any, idempotent bool) error {
				if _, ok := value.(workLogPublicEvent); ok {
					return failure
				}
				return original(dir, name, value, idempotent)
			}
		}, "write successor outbox"},
		{"write successor projection", func(p *transferClaimPorts) {
			p.writeProjection = func(string, workLogProjection) error { return failure }
		}, "injected transfer failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var order []string
			ports := transferClaimTestPorts(t, &order)
			tc.change(&ports)
			if err := ports.transferWorkLogClaim("/home", "/checkout", "commit", "handoff", "next", ClaimExecutionIdentity{Model: "unknown"}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s error = %v, want %q", tc.name, err, tc.want)
			}
		})
	}
	var order []string
	ports := transferClaimTestPorts(t, &order)
	if err := ports.transferWorkLogClaim("/home", "/checkout", "commit", "handoff", "", ClaimExecutionIdentity{Model: "unknown"}); err == nil {
		t.Fatal("empty successor accepted")
	}
	if err := ports.transferWorkLogClaim("/home", "/checkout", "commit", "handoff", "next", ClaimExecutionIdentity{}); err == nil {
		t.Fatal("empty successor model accepted")
	}
}
