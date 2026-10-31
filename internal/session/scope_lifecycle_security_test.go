package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestNativeLifecycleRefusesCorruptParkedEvidenceWithoutAffectingPeer(t *testing.T) {
	t.Parallel()
	for _, corruption := range []string{"json", "schema", "scope", "wbid", "park-id", "timestamp", "oversize", "directory", "hardlink", "permissions"} {
		t.Run(corruption, func(t *testing.T) {
			t.Parallel()
			if corruption == "permissions" && runtime.GOOS == "windows" {
				t.Skip("native Windows ACL contract is exercised by TestWindowsRegistryPrivateScopesAndLifecycle")
			}
			root, caller, peer := sharedScopeFixture(t)
			peerBefore, err := os.ReadFile(recordPath(root, peer.PID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := MarkRecordParked(root, caller, "park-corrupt-evidence"); err != nil {
				t.Fatal(err)
			}
			name, err := scopedLifecycleName(caller, "parked")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, scopeIndexName, caller.Scope, "lifecycle", name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var marker parkedLifecycleMarker
			if err := json.Unmarshal(raw, &marker); err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "json":
				raw = []byte("not-json")
			case "schema":
				marker.SchemaVersion++
			case "scope":
				marker.Scope = peer.Scope
			case "wbid":
				marker.WBSessionID = peer.WBSessionID
			case "park-id":
				marker.ParkedSessionID = ""
			case "timestamp":
				marker.At = time.Time{}
			case "oversize":
				raw = []byte(strings.Repeat("x", (64<<10)+1))
			case "directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, filepath.Join(t.TempDir(), "duplicate-marker")); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if corruption != "json" && corruption != "oversize" {
				raw, err = json.Marshal(marker)
				if err != nil {
					t.Fatal(err)
				}
			}
			if corruption != "directory" && corruption != "hardlink" && corruption != "permissions" {
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, _, _, err := recordLifecycle(root, caller); err == nil {
				t.Fatal("corrupt evidence accepted")
			}
			if _, live, err := LookupActiveRecordExact(root, caller); err == nil || live {
				t.Fatalf("corrupt lifecycle became an active identity: %v", err)
			}
			if _, err := MarkSourceResumed(root, caller, "park-corrupt-evidence", "wbs-untrusted-resume"); err == nil {
				t.Fatal("corrupt source was finalized")
			}
			if err := RequireNoDeparture(root, caller); err == nil {
				t.Fatal("corrupt departure treated as absent")
			}
			peerAfter, err := os.ReadFile(recordPath(root, peer.PID))
			if err != nil || string(peerBefore) != string(peerAfter) {
				t.Fatalf("corruption affected foreign registry: %v", err)
			}
			if _, live, err := LookupActiveRecordExact(root, peer); err != nil || !live {
				t.Fatalf("foreign active identity affected: %v", err)
			}
		})
	}
}

func TestNativeLifecycleRequiresMatchingParkBeforeResumedEvidence(t *testing.T) {
	t.Parallel()
	for _, corruption := range []string{"missing-parent", "json", "schema", "scope", "wbid", "park-id", "successor", "timestamp"} {
		t.Run(corruption, func(t *testing.T) {
			t.Parallel()
			root, caller, _ := sharedScopeFixture(t)
			if _, err := MarkRecordParked(root, caller, "park-resume-evidence"); err != nil {
				t.Fatal(err)
			}
			if _, err := MarkSourceResumed(root, caller, "park-resume-evidence", "wbs-bound-successor"); err != nil {
				t.Fatal(err)
			}
			name, _ := scopedLifecycleName(caller, "resumed")
			path := filepath.Join(root, scopeIndexName, caller.Scope, "lifecycle", name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var marker resumedLifecycleMarker
			if err := json.Unmarshal(raw, &marker); err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "missing-parent":
				parent, _ := scopedLifecycleName(caller, "parked")
				if err := os.Remove(filepath.Join(filepath.Dir(path), parent)); err != nil {
					t.Fatal(err)
				}
			case "json":
				raw = []byte("not-json")
			case "schema":
				marker.SchemaVersion++
			case "scope":
				marker.Scope = "foreign"
			case "wbid":
				marker.WBSessionID = "wbs-foreign"
			case "park-id":
				marker.ParkedSessionID = "other-park"
			case "successor":
				marker.SuccessorWBSessionID = ""
			case "timestamp":
				marker.At = time.Time{}
			}
			if corruption != "json" {
				raw, err = json.Marshal(marker)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, _, _, err := recordLifecycle(root, caller); err == nil {
				t.Fatal("unbound resumed marker accepted")
			}
			if view := sessionView(root, caller); view.State != StateUnknown {
				t.Fatalf("invalid lifecycle is %s", view.State)
			}
		})
	}
}

func TestNativeLifecycleRetriesOnlyTheIdenticalParkAndSuccessor(t *testing.T) {
	t.Parallel()
	root, caller, _ := sharedScopeFixture(t)
	if _, err := markScopedResumed(root, caller, "park-exact", "wbs-successor", nil, time.Now); err == nil {
		t.Fatal("resume before park accepted")
	}
	if _, err := markScopedParked(root, caller, "../invalid", nil, time.Now); err == nil {
		t.Fatal("unsafe parked ID accepted")
	}
	for range 2 {
		if _, err := markScopedParked(root, caller, "park-exact", nil, time.Now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := markScopedParked(root, caller, "park-other", nil, time.Now); err == nil {
		t.Fatal("different parked projection accepted")
	}
	for _, successor := range []string{"", caller.WBSessionID} {
		if _, err := markScopedResumed(root, caller, "park-exact", successor, nil, time.Now); err == nil {
			t.Fatal("invalid successor accepted")
		}
	}
	for range 2 {
		if _, err := markScopedResumed(root, caller, "park-exact", "wbs-successor", nil, time.Now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := markScopedResumed(root, caller, "park-exact", "wbs-other-successor", nil, time.Now); err == nil {
		t.Fatal("different resume winner accepted")
	}
	if _, err := markScopedParked(root, caller, "park-exact", nil, time.Now); err == nil {
		t.Fatal("completed source reparked")
	}
	for _, record := range []Record{{}, {Scope: "forged", Runtime: "codex", NativeHarnessID: "native"}} {
		if _, err := scopedLifecycleName(record, "parked"); err == nil {
			t.Fatal("unqualified lifecycle name accepted")
		}
		if _, err := markScopedParked(root, record, "park-exact", nil, time.Now); err == nil {
			t.Fatal("unqualified park accepted")
		}
		if _, err := markScopedResumed(root, record, "park-exact", "wbs-successor", nil, time.Now); err == nil {
			t.Fatal("unqualified resume accepted")
		}
	}
}

func TestNativeLifecyclePublicationInterruptionRetriesWithoutReplacingEvidence(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("native Windows publisher fault, collision and cleanup ports are exercised separately")
	}
	for _, phase := range []string{"parked", "resumed"} {
		for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepSync, filewrite.StepClose, filewrite.StepRenameNoReplace, filewrite.StepDirSync} {
			t.Run(phase+"/"+string(step), func(t *testing.T) {
				t.Parallel()
				root, caller, _ := sharedScopeFixture(t)
				if phase == "resumed" {
					if _, err := markScopedParked(root, caller, "park-publication", nil, time.Now); err != nil {
						t.Fatal(err)
					}
				}
				name, err := scopedLifecycleName(caller, phase)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, scopeIndexName, caller.Scope, "lifecycle", name)
				failure := errors.New("publication interrupted")
				publish := func(inj *filewrite.Injector) (Record, error) {
					if phase == "parked" {
						return markScopedParked(root, caller, "park-publication", inj, time.Now)
					}
					return markScopedResumed(root, caller, "park-publication", "wbs-publication-successor", inj, time.Now)
				}
				if _, err := publish(&filewrite.Injector{Step: step, Err: failure}); !errors.Is(err, failure) {
					t.Fatalf("interruption lost: %v", err)
				}
				before, beforeErr := os.ReadFile(path)
				if step != filewrite.StepDirSync && !os.IsNotExist(beforeErr) {
					t.Fatalf("prepublication interruption exposed marker: %v", beforeErr)
				}
				result, err := publish(nil)
				if err != nil || result.Lifecycle != phase || result.ParkedSessionID != "park-publication" {
					t.Fatalf("same-authority retry failed: %#v %v", result, err)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if beforeErr == nil && !bytes.Equal(before, after) {
					t.Fatal("post-publication retry replaced immutable evidence")
				}
				if _, _, _, _, err := recordLifecycle(root, caller); err != nil {
					t.Fatalf("retry left invalid lifecycle evidence: %v", err)
				}
			})
		}
	}
}
