package waitrun

import (
	"errors"
	"github.com/sneat-dev/wb/internal/waitregistry"
	"github.com/sneat-dev/wb/internal/worktrees"
	"reflect"
	"testing"
	"time"
)

func TestRegistrationEffectsOrderMetadataIdentityAndBestEffortErrors(t *testing.T) {
	t.Parallel()
	failure := errors.New("metadata refused")
	for _, kind := range []string{"home error", "register error", "unregistered", "registered"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			calls := []string{}
			times := 0
			pidCalls := 0
			at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.FixedZone("local", 3600))
			registry := Registry{EnsureRoot: func(root string) (string, error) {
				calls = append(calls, "home")
				if root != "projects" {
					t.Fatal(root)
				}
				if kind == "home error" {
					return "", failure
				}
				return "home", nil
			}, Now: func() time.Time {
				times++
				calls = append(calls, "now")
				return at.Add(time.Duration(times) * time.Nanosecond)
			}, PID: func() int { pidCalls++; calls = append(calls, "pid"); return 7 }, Identity: func() (worktrees.AgentIdentity, bool) {
				calls = append(calls, "identity")
				return worktrees.AgentIdentity{WBSessionID: "session"}, kind == "registered"
			}, Register: func(home string, record waitregistry.Record) (func(), error) {
				calls = append(calls, "register")
				if home != "home" || record.ID != "7-1790506800000000001" || record.PID != 7 || record.Kind != "pr" || record.Until != "changed" || !reflect.DeepEqual(record.Targets, []string{"acme/app#1"}) || !record.StartedAt.Equal(at.Add(2*time.Nanosecond)) || record.StartedAt.Location() != time.UTC || !record.Deadline.Equal(at.Add(3*time.Nanosecond+time.Minute)) || !reflect.DeepEqual(record.ResumeArgs, []string{"wb", "wait", "pr", "acme/app#1", "--until", "changed"}) {
					t.Fatal(record, home)
				}
				if (record.WBSessionID == "session") != (kind == "registered") {
					t.Fatal(record)
				}
				if kind == "register error" {
					return nil, failure
				}
				return func() { calls = append(calls, "release") }, nil
			}}
			release := registry.RegisterWait(Registration{ProjectsRoot: "projects", Kind: "pr", Targets: []Reference{{Selector: "acme/app#1"}}, Until: "changed", Slice: time.Minute})
			if release == nil {
				t.Fatal("nil release")
			}
			release()
			if kind == "home error" {
				if !reflect.DeepEqual(calls, []string{"home"}) {
					t.Fatal(calls)
				}
			} else {
				expected := []string{"home", "pid", "now", "pid", "now", "now", "identity", "register"}
				if kind != "register error" {
					expected = append(expected, "release")
				}
				if !reflect.DeepEqual(calls, expected) || times != 3 || pidCalls != 2 {
					t.Fatal(calls, expected, times, pidCalls)
				}
			}
		})
	}
}
func TestInspectHomeListPruneErrorsAndPrecedence(t *testing.T) {
	t.Parallel()
	failure := errors.New("inspect refused")
	for _, kind := range []string{"home", "list", "prune", "success list", "success prune"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			prune := kind == "prune" || kind == "success prune"
			calls := []string{}
			registry := Registry{EnsureRoot: func(string) (string, error) {
				calls = append(calls, "home")
				if kind == "home" {
					return "", failure
				}
				return "home", nil
			}, List: func(home string, opt waitregistry.Options) ([]waitregistry.Record, error) {
				calls = append(calls, "list")
				if home != "home" || opt.Alive != nil {
					t.Fatal(home, opt)
				}
				if kind == "list" {
					return nil, failure
				}
				return []waitregistry.Record{{ID: "live"}}, nil
			}, Prune: func(home string, opt waitregistry.Options) (int, error) {
				calls = append(calls, "prune")
				if home != "home" || opt.Alive != nil {
					t.Fatal(home, opt)
				}
				if kind == "prune" {
					return 0, failure
				}
				return 2, nil
			}}
			result, err := registry.Inspect(ListRequest{Prune: prune})
			if kind == "home" || kind == "list" || kind == "prune" {
				if err != failure {
					t.Fatal(err)
				}
			} else if err != nil || result.Pruned != prune || (prune && result.Removed != 2) || (!prune && result.Records[0].ID != "live") {
				t.Fatal(result, err)
			}
			if len(calls) != map[bool]int{true: 1, false: 2}[kind == "home"] {
				t.Fatal(calls)
			}
		})
	}
}
