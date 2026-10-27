package worktrees

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreecollab"
)

func custodyTestCheckout() worktreecollab.Checkout {
	return worktreecollab.Checkout{ID: "wt-" + strings.Repeat("a", 64), Root: "/checkout", GitDir: "/checkout/.git", CommonDir: "/canonical/.git"}
}

func custodyTestState(owner string, epoch, revision uint64) worktreecollab.State {
	return worktreecollab.State{Version: worktreecollab.SchemaVersion, Checkout: custodyTestCheckout(), Owner: owner, OwnerEpoch: epoch, Revision: revision,
		Members: map[string]worktreecollab.Member{owner: {}}, Notices: map[string]worktreecollab.OwnerChange{},
		Inboxes: map[string][]worktreecollab.Message{}, Requests: map[string]worktreecollab.SendReceipt{}, Cursors: map[string]uint64{}}
}

func custodyTestPorts() retireCustodyPorts {
	return retireCustodyPorts{
		resolve: func(context.Context, string, string) (worktreecollab.Checkout, error) {
			return custodyTestCheckout(), nil
		},
		load: func(string, worktreecollab.Checkout) (worktreecollab.State, bool, error) {
			return custodyTestState("wbs-owner", 2, 3), true, nil
		},
		identity: func() AgentIdentity {
			return AgentIdentity{WBSessionID: "wbs-owner", Registered: true, PID: 42}
		},
		liveSession: func(string, string) error { return nil },
	}
}

func TestRetireCustodyAcceptsOnlyCorroboratedCurrentOwner(t *testing.T) {
	t.Parallel()
	entry := ListResult{WorktreeDir: "/checkout"}
	fence, err := retireCheckCustodyWith(context.Background(), "/projects", "/projects/.wb", entry, custodyTestPorts())
	if err != nil {
		t.Fatalf("retireCheckCustodyWith: %v", err)
	}
	want := retireCustodyFence{mode: "collaboration", sessionID: "wbs-owner", epoch: 2, revision: 3}
	if fence != want {
		t.Fatalf("custody fence = %+v, want %+v", fence, want)
	}
	if !fence.same(want) || fence.same(retireCustodyFence{mode: "collaboration", sessionID: "wbs-owner", epoch: 2, revision: 4}) {
		t.Fatal("custody fence equality does not bind epoch and revision")
	}
	if fence.same(retireCustodyFence{mode: "legacy"}) {
		t.Fatal("legacy custody compared equal to collaboration custody")
	}
}

func TestRetireCustodyRefusesUncorroboratedAuthority(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected custody failure")
	legacyOwners := []OwnerView{{OwnerRegistration: OwnerRegistration{PID: 77}, PIDStatus: "active"}}
	cases := []struct {
		name   string
		entry  ListResult
		change func(*retireCustodyPorts)
		want   string
	}{
		{"checkout resolution", ListResult{WorktreeDir: "/checkout"}, func(p *retireCustodyPorts) {
			p.resolve = func(context.Context, string, string) (worktreecollab.Checkout, error) {
				return worktreecollab.Checkout{}, boom
			}
		}, "custody checkout"},
		{"authority load", ListResult{WorktreeDir: "/checkout"}, func(p *retireCustodyPorts) {
			p.load = func(string, worktreecollab.Checkout) (worktreecollab.State, bool, error) {
				return worktreecollab.State{}, false, boom
			}
		}, "custody authority"},
		{"legacy competing owner", ListResult{WorktreeDir: "/checkout", Owners: legacyOwners}, func(p *retireCustodyPorts) {
			p.load = func(string, worktreecollab.Checkout) (worktreecollab.State, bool, error) {
				return worktreecollab.State{}, false, nil
			}
		}, "competing active claim"},
		{"unregistered caller", ListResult{WorktreeDir: "/checkout"}, func(p *retireCustodyPorts) {
			p.identity = func() AgentIdentity { return AgentIdentity{PID: 42} }
		}, "registered invoking session"},
		{"different current owner", ListResult{WorktreeDir: "/checkout"}, func(p *retireCustodyPorts) {
			p.identity = func() AgentIdentity { return AgentIdentity{WBSessionID: "wbs-other", Registered: true} }
		}, "requires the corroborated current owner"},
		{"missing audited epoch", ListResult{WorktreeDir: "/checkout"}, func(p *retireCustodyPorts) {
			p.load = func(string, worktreecollab.Checkout) (worktreecollab.State, bool, error) {
				return custodyTestState("wbs-owner", 0, 1), true, nil
			}
		}, "audited owner epoch"},
		{"missing revision", ListResult{WorktreeDir: "/checkout"}, func(p *retireCustodyPorts) {
			p.load = func(string, worktreecollab.Checkout) (worktreecollab.State, bool, error) {
				return custodyTestState("wbs-owner", 1, 0), true, nil
			}
		}, "audited owner epoch"},
		{"uncorroborated live session", ListResult{WorktreeDir: "/checkout"}, func(p *retireCustodyPorts) {
			p.liveSession = func(string, string) error { return boom }
		}, "current owner session"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ports := custodyTestPorts()
			tt.change(&ports)
			if _, err := retireCheckCustodyWith(context.Background(), "/projects", "/projects/.wb", tt.entry, ports); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("custody error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestRetireCustodyRetainsLegacyPolicyWithoutAuthority(t *testing.T) {
	t.Parallel()
	ports := custodyTestPorts()
	ports.load = func(string, worktreecollab.Checkout) (worktreecollab.State, bool, error) {
		return worktreecollab.State{}, false, nil
	}
	ports.identity = func() AgentIdentity { return AgentIdentity{PID: 42} }
	fence, err := retireCheckCustodyWith(context.Background(), "/projects", "/projects/.wb", ListResult{WorktreeDir: "/checkout"}, ports)
	if err != nil || fence.mode != "legacy" {
		t.Fatalf("legacy custody = (%+v, %v)", fence, err)
	}
}

func TestCorroborateRetireCustodySessionRefusesUnavailableSessions(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected session failure")
	live := session.Record{WBSessionID: "wbs-owner", PID: 42}
	cases := []struct {
		name  string
		ports retireCustodySessionPorts
		want  string
	}{
		{"list failure", retireCustodySessionPorts{list: func(string) ([]session.View, error) { return nil, boom }}, "injected session failure"},
		{"no match", retireCustodySessionPorts{list: func(string) ([]session.View, error) { return nil, nil }}, "ambiguous or unavailable"},
		{"duplicate match", retireCustodySessionPorts{list: func(string) ([]session.View, error) {
			return []session.View{{Record: live}, {Record: live}}, nil
		}}, "ambiguous or unavailable"},
		{"missing pid", retireCustodySessionPorts{list: func(string) ([]session.View, error) {
			return []session.View{{Record: session.Record{WBSessionID: "wbs-owner"}}}, nil
		}}, "ambiguous or unavailable"},
		{"lookup failure", retireCustodySessionPorts{
			list:        func(string) ([]session.View, error) { return []session.View{{Record: live}}, nil },
			lookupExact: func(string, int) (session.Record, bool, error) { return session.Record{}, false, boom },
		}, "injected session failure"},
		{"not live", retireCustodySessionPorts{
			list:        func(string) ([]session.View, error) { return []session.View{{Record: live}}, nil },
			lookupExact: func(string, int) (session.Record, bool, error) { return live, false, nil },
		}, "not live"},
		{"exact identity differs", retireCustodySessionPorts{
			list: func(string) ([]session.View, error) { return []session.View{{Record: live}}, nil },
			lookupExact: func(string, int) (session.Record, bool, error) {
				return session.Record{WBSessionID: "wbs-other", PID: 42}, true, nil
			},
		}, "not live"},
		{"lifecycle transition", retireCustodySessionPorts{
			list: func(string) ([]session.View, error) { return []session.View{{Record: live}}, nil },
			lookupExact: func(string, int) (session.Record, bool, error) {
				return session.Record{WBSessionID: "wbs-owner", PID: 42, Lifecycle: "parked"}, true, nil
			},
		}, "not live"},
		{"corroborated", retireCustodySessionPorts{
			list:        func(string) ([]session.View, error) { return []session.View{{Record: live}}, nil },
			lookupExact: func(string, int) (session.Record, bool, error) { return live, true, nil },
		}, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := corroborateRetireCustodySessionWith("/projects/.wb", "wbs-owner", tt.ports)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("corroborate session: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("session error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
