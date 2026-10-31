package session

import (
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/sessionterminal"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func currentTransitionFixture(t *testing.T) (string, Record, Record) {
	t.Helper()
	dir := t.TempDir()
	caller, err := Register(dir, Record{PID: os.Getpid(), Runtime: "codex", NativeHarnessID: "native-current", Model: Unknown})
	if err != nil {
		t.Fatal(err)
	}
	if caller.ProcessStartedAt.IsZero() {
		t.Fatal("native process identity unavailable")
	}
	successor := caller
	successor.WBSessionID, successor.PredecessorWBSessionID, successor.HandoffID = "wbs-successor", caller.WBSessionID, "park-current"
	successor.StartedAt = caller.StartedAt.Add(time.Second)
	return dir, caller, successor
}

func TestCurrentIdentitySurvivesJSONAndNativeGenerationProofIsStrict(t *testing.T) {
	t.Parallel()
	dir, caller, _ := currentTransitionFixture(t)
	caller.Terminal = sessionterminal.Identity{Kind: "herdr", Socket: filepath.Join(dir, "server.sock"), WorkspaceID: "w1", PaneID: "w1:p1"}
	raw, err := json.Marshal(caller)
	if err != nil {
		t.Fatal(err)
	}
	var restored Record
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored != caller {
		t.Fatal("terminal record identity changed across serialization")
	}
	if err := ProveGenerationGone(caller); err == nil {
		t.Fatal("live actor classified gone")
	}
	unknown := caller
	unknown.ProcessStartedAt = time.Time{}
	if err := ProveGenerationGone(unknown); err == nil {
		t.Fatal("unknown actor classified gone")
	}
	recycled := caller
	recycled.ProcessStartedAt = recycled.ProcessStartedAt.Add(-time.Hour)
	if err := ProveGenerationGone(recycled); err != nil {
		t.Fatalf("old recycled generation not proven gone: %v", err)
	}
}

func TestCurrentTransitionRetriesBeforeAndAfterRegistryCommit(t *testing.T) {
	t.Parallel()
	for _, afterCommit := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-commit", true: "after-commit"}[afterCommit], func(t *testing.T) {
			dir, caller, successor := currentTransitionFixture(t)
			want := errors.New("interrupted pickup")
			if err := WithCurrentTransition(dir, caller, successor, func(commit func() error) error {
				if afterCommit {
					if err := commit(); err != nil {
						return err
					}
				}
				return want
			}); !errors.Is(err, want) {
				t.Fatalf("interruption: %v", err)
			}
			if err := WithCurrentTransition(dir, caller, successor, func(commit func() error) error { return commit() }); err != nil {
				t.Fatal(err)
			}
			if err := WithCurrentTransition(dir, caller, successor, func(commit func() error) error { return commit() }); err != nil {
				t.Fatal(err)
			}
			exact, live, err := LookupExact(dir, caller.PID)
			if err != nil || !live || exact.WBSessionID != successor.WBSessionID {
				t.Fatalf("successor missing: %#v %v", exact, err)
			}
		})
	}
}

func TestCurrentTransitionRefusesUnrelatedReregistration(t *testing.T) {
	t.Parallel()
	dir, caller, successor := currentTransitionFixture(t)
	unrelated := caller
	unrelated.Model = "different-model"
	if _, err := Register(dir, unrelated); err != nil {
		t.Fatal(err)
	}
	called := false
	err := WithCurrentTransition(dir, caller, successor, func(commit func() error) error { called = true; return commit() })
	if err == nil || called {
		t.Fatalf("unrelated registration admitted: %v", err)
	}
}

func TestCurrentTransitionSerializesConcurrentRegistration(t *testing.T) {
	t.Parallel()
	dir, caller, successor := currentTransitionFixture(t)
	started, finished := make(chan struct{}), make(chan error, 1)
	err := WithCurrentTransition(dir, caller, successor, func(commit func() error) error {
		go func() {
			close(started)
			_, err := Register(dir, Record{PID: caller.PID, WBSessionID: "wbs-other", Runtime: "codex", NativeHarnessID: "other-native"})
			finished <- err
		}()
		<-started
		select {
		case err := <-finished:
			t.Fatalf("registration crossed retained PID barrier: %v", err)
		case <-time.After(25 * time.Millisecond):
		}
		return commit()
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not leave released barrier")
	}
	exact, live, err := LookupExact(dir, caller.PID)
	if err != nil || !live || exact.WBSessionID != "wbs-other" {
		t.Fatalf("later registration lost: %#v %v", exact, err)
	}
}

func TestSourceFinalizationNeverRestoresSamePIDParkedRecord(t *testing.T) {
	t.Parallel()
	dir, caller, successor := currentTransitionFixture(t)
	if _, err := MarkParked(dir, caller.PID, successor.HandoffID); err != nil {
		t.Fatal(err)
	}
	err := WithCurrentTransition(dir, caller, successor, func(commit func() error) error {
		if err := commit(); err != nil {
			return err
		}
		_, err := MarkSourceResumed(dir, caller, successor.HandoffID, successor.WBSessionID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkSourceResumed(dir, caller, successor.HandoffID, successor.WBSessionID); err != nil {
		t.Fatal(err)
	}
	exact, live, err := LookupExact(dir, caller.PID)
	if err != nil || !live || exact.WBSessionID != successor.WBSessionID {
		t.Fatalf("finalization restored source: %#v %v", exact, err)
	}
}

func TestCurrentResolutionRequiresAnObservedRegisteredHarness(t *testing.T) {
	t.Parallel()
	dir, caller, _ := currentTransitionFixture(t)
	for _, test := range []struct {
		name           string
		pid            int
		runtime        string
		found, refused bool
	}{
		{"registered-shell-is-not-harness", 0, "", false, false},
		{"unsupported-runtime", caller.PID, "shell", false, false},
		{"different-runtime", caller.PID, "claude-code", false, true},
		{"unregistered-harness", caller.PID + 1000000, "codex", false, false},
		{"observed-exact-harness", caller.PID, "codex", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, found, err := resolveCurrentForProcess(dir, os.Getpid(), func(int) (int, string) { return test.pid, test.runtime }, func(string) (string, bool) { return caller.NativeHarnessID, true })
			if found != test.found || (err != nil) != test.refused || found && got.WBSessionID != caller.WBSessionID {
				t.Fatalf("resolution = %#v %t %v", got, found, err)
			}
		})
	}
}

func TestCurrentIdentityUsesThreadAndDoesNotConflateSharedRoot(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		thread, session string
		refused         bool
	}{
		{"expected", "expected", false}, {"expected", "", false}, {"", "expected", true}, {"expected", "other", false}, {"other", "expected", true}, {"", "", true},
	} {
		t.Run(test.thread+"-"+test.session, func(t *testing.T) {
			err := matchCurrentNative(Record{Runtime: "codex", NativeHarnessID: "expected"}, func(key string) (string, bool) {
				if key == "CODEX_THREAD_ID" {
					return test.thread, test.thread != ""
				}
				if key == "CODEX_SESSION_ID" {
					return test.session, test.session != ""
				}
				return "", false
			})
			if (err != nil) != test.refused {
				t.Fatalf("per-chat identity refusal=%t: %v", test.refused, err)
			}
		})
	}
}
