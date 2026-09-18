package sessionpark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestSpCovSourceLockAuthenticatesOnlyExactAggregateBytes(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	raw, err := EncodeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := string(sessionmove.DigestBytes(raw))

	if !lock.HeldForSession(store.Root, bundle.ParkedSessionID, digest) {
		t.Fatal("held lock did not authenticate its exact aggregate")
	}
	if lock.HeldForSession(store.Root, bundle.ParkedSessionID, "sha256:"+strings.Repeat("0", 64)) {
		t.Fatal("held lock authenticated a digest it never admitted")
	}
	if lock.HeldForSession(store.Root, "park-other", digest) {
		t.Fatal("held lock authenticated a foreign aggregate ID")
	}
	if lock.HeldForSession(filepath.Join(store.Root, "elsewhere"), bundle.ParkedSessionID, digest) {
		t.Fatal("held lock authenticated a foreign store root")
	}
	if !lock.held(store.Root, bundle.ParkedSessionID) || lock.held(store.Root, "park-other") {
		t.Fatal("unexported held() disagreed with HeldForSession")
	}
	if !EqualBundle(lock.Bundle(), bundle) {
		t.Fatalf("lock bundle = %#v", lock.Bundle())
	}

	// Close is the only lock method that tolerates a nil receiver; the
	// authentication methods deliberately lock their mutex first.
	var nilLock *SourceLock
	if err := nilLock.Close(); err != nil {
		t.Fatalf("nil source lock close = %v", err)
	}
	var nilTarget *TargetLock
	if err := nilTarget.Close(); err != nil {
		t.Fatalf("nil target lock close = %v", err)
	}
}

func TestSpCovSourceLockRejectsTamperedAggregateEvidence(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, store Store, bundle Bundle){
		"root mode": func(t *testing.T, store Store, bundle Bundle) {
			if err := os.Chmod(store.Root, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"root replaced": func(t *testing.T, store Store, bundle Bundle) {
			backup := store.Root + "-backup"
			if err := os.Rename(store.Root, backup); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(store.Root, 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"aggregate removed": func(t *testing.T, store Store, bundle Bundle) {
			if err := os.RemoveAll(spCovAggregatePath(store.Root, bundle.ParkedSessionID)); err != nil {
				t.Fatal(err)
			}
		},
		"aggregate replaced": func(t *testing.T, store Store, bundle Bundle) {
			agg := spCovAggregatePath(store.Root, bundle.ParkedSessionID)
			if err := os.Rename(agg, agg+"-backup"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(agg, 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"bundle removed": func(t *testing.T, store Store, bundle Bundle) {
			if err := os.Remove(filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceBundleFileName)); err != nil {
				t.Fatal(err)
			}
		},
		"bundle replaced": func(t *testing.T, store Store, bundle Bundle) {
			path := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceBundleFileName)
			if err := os.Rename(path, path+"-backup"); err != nil {
				t.Fatal(err)
			}
			spCovWriteRaw(t, path, []byte("{}\n"), 0o600)
		},
		"bundle content changed": func(t *testing.T, store Store, bundle Bundle) {
			spCovWriteRaw(t, filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceBundleFileName), []byte("{}\n"), 0o600)
		},
		"lock removed": func(t *testing.T, store Store, bundle Bundle) {
			if err := os.Remove(filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceResumeLockName)); err != nil {
				t.Fatal(err)
			}
		},
		"lock replaced": func(t *testing.T, store Store, bundle Bundle) {
			path := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceResumeLockName)
			if err := os.Rename(path, path+"-backup"); err != nil {
				t.Fatal(err)
			}
			spCovWriteRaw(t, path, nil, 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, bundle := spCovCreatedStore(t)
			lock := spCovAcquire(t, store, bundle.ParkedSessionID)
			raw, err := EncodeBundle(bundle)
			if err != nil {
				t.Fatal(err)
			}
			digest := string(sessionmove.DigestBytes(raw))
			mutate(t, store, bundle)
			if lock.HeldForSession(store.Root, bundle.ParkedSessionID, digest) {
				t.Fatal("tampered aggregate evidence still authenticated")
			}
		})
	}
}

func TestSpCovRetainSessionDirAndUnderLockReads(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	raw, err := EncodeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := string(sessionmove.DigestBytes(raw))

	if _, err := store.LoadUnderLock(nil); err == nil {
		t.Fatal("load without authority accepted")
	}
	if _, err := store.LoadUnderLock(&SourceLock{}); err == nil {
		t.Fatal("load with zero lock accepted")
	}
	if _, err := store.ContinuationPathUnderLock(nil); err == nil {
		t.Fatal("continuation without authority accepted")
	}
	if _, _, err := store.EnsureLocalSuccessorContextUnderLock(nil, nil); err == nil {
		t.Fatal("successor context without authority accepted")
	}
	if _, _, _, err := store.LoadLocalSuccessorContextUnderLock(nil); err == nil {
		t.Fatal("local successor read without authority accepted")
	}
	if _, _, err := store.ExistingLocalLaunchRootUnderLock(nil); err == nil {
		t.Fatal("existing launch root without authority accepted")
	}
	if _, err := store.LocalLaunchRootUnderLock(nil); err == nil {
		t.Fatal("launch root without authority accepted")
	}
	if _, err := store.ResumeUnderLock(nil, session.Record{PID: 1, WBSessionID: "wbs-x"}, time.Now()); err == nil {
		t.Fatal("resume without authority accepted")
	}

	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	if _, err := lock.RetainSessionDir(store.Root, bundle.ParkedSessionID, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("retain with wrong digest accepted")
	}
	if _, err := lock.RetainSessionDir(store.Root, "park-other", digest); err == nil {
		t.Fatal("retain with wrong park ID accepted")
	}
	retained, err := lock.RetainSessionDir(store.Root, bundle.ParkedSessionID, digest)
	if err != nil || retained == nil {
		t.Fatalf("retain = %v err=%v", retained, err)
	}
	if err := retained.Close(); err != nil {
		t.Fatal(err)
	}

	state, err := store.LoadUnderLock(lock)
	if err != nil || state.Status != StatusParked || !EqualBundle(state.Bundle, bundle) {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	path, err := store.ContinuationPathUnderLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(store.Root, bundle.ParkedSessionID, sourceContinuationFileName)
	if path != want {
		t.Fatalf("continuation path = %q want %q", path, want)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != bundle.Continuation {
		t.Fatalf("continuation content = %q err=%v", content, err)
	}
	spCovWriteRaw(t, path, []byte("tampered"), 0o600)
	if _, err := store.ContinuationPathUnderLock(lock); err == nil {
		t.Fatal("tampered continuation accepted")
	}
}

func TestSpCovEnsureAndLoadLocalSuccessorContext(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	if _, _, err := store.EnsureLocalSuccessorContextUnderLock(lock, nil); err == nil {
		t.Fatal("successor context published without a claimed route")
	}
	if _, _, _, err := store.LoadLocalSuccessorContextUnderLock(lock); err == nil {
		t.Fatal("successor context read without a claimed route")
	}
	if _, _, err := store.PrepareLocalUnderLock(lock, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if _, raw, found, err := store.LoadLocalSuccessorContextUnderLock(lock); err != nil || found || raw != nil {
		t.Fatalf("absent context raw=%q found=%t err=%v", raw, found, err)
	}
	path, raw, err := store.EnsureLocalSuccessorContextUnderLock(lock, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(store.Root, bundle.ParkedSessionID, SuccessorContextFileName)
	if path != wantPath {
		t.Fatalf("context path = %q want %q", path, wantPath)
	}
	body := string(raw)
	if !strings.HasPrefix(body, bundle.Continuation) || !strings.Contains(body, "- member-001 acme/app") ||
		!strings.Contains(body, "path: /tmp/app-a") || !strings.Contains(body, "branch: feature/a") {
		t.Fatalf("successor context body = %q", body)
	}
	fileRaw, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(fileRaw, raw) {
		t.Fatalf("published context differs from returned bytes: err=%v", err)
	}
	loadPath, loadRaw, found, err := store.LoadLocalSuccessorContextUnderLock(lock)
	if err != nil || !found || loadPath != path || !bytes.Equal(loadRaw, raw) {
		t.Fatalf("load path=%q found=%t err=%v", loadPath, found, err)
	}
	spCovWriteRaw(t, path, []byte("conflicting context"), 0o600)
	if _, _, _, err := store.LoadLocalSuccessorContextUnderLock(lock); err == nil {
		t.Fatal("conflicting successor context accepted")
	}
	if _, _, err := store.EnsureLocalSuccessorContextUnderLock(lock, nil); err == nil {
		t.Fatal("conflicting successor context republished")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.LoadLocalSuccessorContextUnderLock(lock); err == nil {
		t.Fatal("symlinked successor context accepted")
	}
}

func TestSpCovLocalSuccessorContextZeroMembersAndOversize(t *testing.T) {
	t.Run("zero members", func(t *testing.T) {
		bundle := testBundle(t)
		bundle.ParkedSessionID = "park-neutral"
		bundle.Worktrees = nil
		store := NewStore(t.TempDir())
		if _, err := store.Create(bundle); err != nil {
			t.Fatal(err)
		}
		lock := spCovAcquire(t, store, bundle.ParkedSessionID)
		if _, _, err := store.PrepareLocalUnderLock(lock, time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
		path, raw, err := store.EnsureLocalSuccessorContextUnderLock(lock, nil)
		if err != nil {
			t.Fatal(err)
		}
		if path != filepath.Join(store.Root, bundle.ParkedSessionID, SuccessorContextFileName) ||
			!strings.Contains(string(raw), "- none\n") {
			t.Fatalf("neutral context path=%q raw=%q", path, raw)
		}
	})
	t.Run("oversized body", func(t *testing.T) {
		bundle := testBundle(t)
		bundle.ParkedSessionID = "park-huge-context"
		bundle.Worktrees = []Worktree{{
			Repository: "acme/app", WorktreeDir: "/" + strings.Repeat("h", MaxSuccessorContextBytes+1024),
			Branch: "feature/huge", Head: strings.Repeat("a", 40),
		}}
		store := NewStore(t.TempDir())
		if _, err := store.Create(bundle); err != nil {
			t.Fatal(err)
		}
		lock := spCovAcquire(t, store, bundle.ParkedSessionID)
		if _, _, err := store.PrepareLocalUnderLock(lock, time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.EnsureLocalSuccessorContextUnderLock(lock, nil); err == nil {
			t.Fatal("oversized successor context accepted")
		}
	})
}

func TestSpCovLocalLaunchRootSelectsMemberOrNeutralRoot(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	if _, err := store.LocalLaunchRootUnderLock(lock); err == nil {
		t.Fatal("launch root selected without a claimed route")
	}
	if _, _, err := store.ExistingLocalLaunchRootUnderLock(lock); err == nil {
		t.Fatal("existing launch root read without a claimed route")
	}
	if _, _, err := store.PrepareLocalUnderLock(lock, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	root, err := store.LocalLaunchRootUnderLock(lock)
	if err != nil || root != bundle.Worktrees[0].WorktreeDir {
		t.Fatalf("launch root = %q err=%v", root, err)
	}
	existing, ok, err := store.ExistingLocalLaunchRootUnderLock(lock)
	if err != nil || !ok || existing != bundle.Worktrees[0].WorktreeDir {
		t.Fatalf("existing root = %q ok=%t err=%v", existing, ok, err)
	}
}

func TestSpCovLocalNeutralRootCreationReuseAndRejection(t *testing.T) {
	newNeutralLock := func(t *testing.T) (Store, Bundle, *SourceLock) {
		t.Helper()
		bundle := testBundle(t)
		bundle.ParkedSessionID = "park-neutral"
		bundle.Worktrees = nil
		store := NewStore(t.TempDir())
		if _, err := store.Create(bundle); err != nil {
			t.Fatal(err)
		}
		lock := spCovAcquire(t, store, bundle.ParkedSessionID)
		if _, _, err := store.PrepareLocalUnderLock(lock, time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
		return store, bundle, lock
	}

	t.Run("creates then reuses", func(t *testing.T) {
		store, bundle, lock := newNeutralLock(t)
		if existing, ok, err := store.ExistingLocalLaunchRootUnderLock(lock); err != nil || ok || existing != "" {
			t.Fatalf("existing=%q ok=%t err=%v", existing, ok, err)
		}
		first, err := store.LocalLaunchRootUnderLock(lock)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(store.Root, bundle.ParkedSessionID, LocalNeutralDirName)
		if first != want {
			t.Fatalf("neutral root = %q want %q", first, want)
		}
		info, err := os.Stat(first)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("neutral root info=%v err=%v", info, err)
		}
		second, err := store.LocalLaunchRootUnderLock(lock)
		if err != nil || second != first {
			t.Fatalf("second neutral root = %q err=%v", second, err)
		}
		existing, ok, err := store.ExistingLocalLaunchRootUnderLock(lock)
		if err != nil || !ok || existing != first {
			t.Fatalf("existing=%q ok=%t err=%v", existing, ok, err)
		}
	})
	t.Run("regular file rejected", func(t *testing.T) {
		store, bundle, lock := newNeutralLock(t)
		path := filepath.Join(store.Root, bundle.ParkedSessionID, LocalNeutralDirName)
		spCovWriteRaw(t, path, []byte("not a directory"), 0o600)
		if _, err := store.LocalLaunchRootUnderLock(lock); err == nil {
			t.Fatal("regular file accepted as neutral root")
		}
		if _, _, err := store.ExistingLocalLaunchRootUnderLock(lock); err == nil {
			t.Fatal("regular file accepted as existing neutral root")
		}
	})
}

func TestSpCovResumeUnderLockValidatesSuccessorLineage(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	if _, err := store.ResumeUnderLock(lock, session.Record{PID: 1, WBSessionID: "wbs-x"}, time.Now()); err == nil {
		t.Fatal("resume without a claimed route accepted")
	}
	if _, _, err := store.PrepareLocalUnderLock(lock, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	for name, successor := range map[string]session.Record{
		"zero pid":          {PID: 0, WBSessionID: "wbs-x"},
		"invalid session":   {PID: 1, WBSessionID: ".."},
		"same as source":    {PID: 1, WBSessionID: bundle.Source.WBSessionID},
		"foreign lineage":   {PID: 1, WBSessionID: "wbs-x", PredecessorWBSessionID: "wbs-foreign"},
		"invalid ancestor":  {PID: 1, WBSessionID: "wbs-x", PredecessorWBSessionID: ".."},
		"negative pid":      {PID: -3, WBSessionID: "wbs-x"},
		"padded session id": {PID: 1, WBSessionID: " wbs-x"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.ResumeUnderLock(lock, successor, time.Now()); err == nil {
				t.Fatalf("invalid successor accepted: %#v", successor)
			}
		})
	}
	state, err := store.ResumeUnderLock(lock, session.Record{PID: 42, WBSessionID: "wbs-x"}, time.Time{})
	if err != nil || state.Status != StatusResumed || state.Successor == nil || state.Successor.WBSessionID != "wbs-x" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	if state.Successor.PredecessorWBSessionID != bundle.Source.WBSessionID {
		t.Fatalf("lineage not defaulted: %#v", state.Successor)
	}
	if state.Events[0].At.IsZero() || state.Events[0].Sequence != 1 || state.Events[0].Type != "resumed" {
		t.Fatalf("resumed event = %#v", state.Events[0])
	}
	retry, err := store.ResumeUnderLock(lock, session.Record{PID: 43, WBSessionID: "wbs-y"}, time.Now())
	if err != nil || retry.Successor == nil || retry.Successor.WBSessionID != "wbs-x" || len(retry.Events) != 1 {
		t.Fatalf("retry state=%#v err=%v", retry, err)
	}
}

func TestSpCovClaimResumeRouteRejectsUnsafeModesAndTargets(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	local := func() (ResumeRoute, bool, error) {
		return store.claimResumeRouteUnderLock(lock, ResumeRouteLocal, "", "", sessionmove.SSHConfig{}, time.Now())
	}
	if _, _, err := store.claimResumeRouteUnderLock(nil, ResumeRouteLocal, "", "", sessionmove.SSHConfig{}, time.Now()); err == nil {
		t.Fatal("nil lock claimed a route")
	}
	if _, _, err := store.claimResumeRouteUnderLock(lock, "bogus", "", "", sessionmove.SSHConfig{}, time.Now()); err == nil {
		t.Fatal("invalid mode claimed a route")
	}
	if _, _, err := store.claimResumeRouteUnderLock(lock, ResumeRouteLocal, "target", "", sessionmove.SSHConfig{}, time.Now()); err == nil {
		t.Fatal("local route carrying a target claimed")
	}
	if _, _, err := store.claimResumeRouteUnderLock(lock, ResumeRouteLocal, "", "ssh", sessionmove.SSHConfig{}, time.Now()); err == nil {
		t.Fatal("local route carrying a courier claimed")
	}
	if _, _, err := store.claimResumeRouteUnderLock(lock, ResumeRouteLocal, "", "", sessionmove.SSHConfig{Host: "target.example"}, time.Now()); err == nil {
		t.Fatal("local route carrying SSH config claimed")
	}
	for name, call := range map[string]func() (ResumeRoute, bool, error){
		"remote invalid target": func() (ResumeRoute, bool, error) {
			return store.claimResumeRouteUnderLock(lock, ResumeRouteRemote, "..", string(sessionmove.CourierSSH), testParkedSSH(), time.Now())
		},
		"remote wrong courier": func() (ResumeRoute, bool, error) {
			return store.claimResumeRouteUnderLock(lock, ResumeRouteRemote, "target", "loopback", testParkedSSH(), time.Now())
		},
		"remote custom path": func() (ResumeRoute, bool, error) {
			return store.claimResumeRouteUnderLock(lock, ResumeRouteRemote, "target", string(sessionmove.CourierSSH), sessionmove.SSHConfig{Host: "target.example", WBPath: "/opt/wb"}, time.Now())
		},
		"remote invalid ssh": func() (ResumeRoute, bool, error) {
			return store.claimResumeRouteUnderLock(lock, ResumeRouteRemote, "target", string(sessionmove.CourierSSH), sessionmove.SSHConfig{}, time.Now())
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := call(); err == nil {
				t.Fatal("unsafe remote route claimed")
			}
		})
	}
	corrupt := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceResumeRouteFileName)
	spCovWriteRaw(t, corrupt, []byte("{}\n"), 0o600)
	if _, _, err := local(); err == nil {
		t.Fatal("corrupt durable route accepted")
	}
	if err := os.Remove(corrupt); err != nil {
		t.Fatal(err)
	}
	route, replay, err := local()
	if err != nil || replay || route.Mode != ResumeRouteLocal || route.ClaimedAt.IsZero() {
		t.Fatalf("route=%#v replay=%t err=%v", route, replay, err)
	}
	stored, replay, err := local()
	if err != nil || !replay || stored != route {
		t.Fatalf("stored=%#v replay=%t err=%v", stored, replay, err)
	}
	if _, _, err := store.claimResumeRouteUnderLock(lock, ResumeRouteRemote, "target", string(sessionmove.CourierSSH), testParkedSSH(), time.Now()); err == nil {
		t.Fatal("competing remote route claimed")
	}
}

func TestSpCovResumeRouteValidationAndSSHConfig(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	if _, err := store.validateResumeRouteUnderLock(nil, ResumeRouteLocal, ""); err == nil {
		t.Fatal("nil lock validated a route")
	}
	if _, err := store.validateResumeRouteUnderLock(lock, ResumeRouteLocal, ""); err == nil {
		t.Fatal("absent route validated")
	}
	routePath := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceResumeRouteFileName)
	spCovWriteRaw(t, routePath, []byte("{}\n"), 0o600)
	if _, err := store.validateResumeRouteUnderLock(lock, ResumeRouteLocal, ""); err == nil {
		t.Fatal("corrupt route validated")
	}
	if err := os.Remove(routePath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PrepareLocalUnderLock(lock, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if route, err := store.validateResumeRouteUnderLock(lock, ResumeRouteLocal, ""); err != nil || route.Mode != ResumeRouteLocal {
		t.Fatalf("route=%#v err=%v", route, err)
	}
	if _, err := store.validateResumeRouteUnderLock(lock, ResumeRouteRemote, "target"); err == nil || !strings.Contains(err.Error(), "not remote:target") {
		t.Fatalf("mismatched route error = %v", err)
	}

	if _, err := (ResumeRoute{Mode: ResumeRouteLocal}).SSHConfig(); err == nil {
		t.Fatal("local route exposed SSH config")
	}
	remote := ResumeRoute{Mode: ResumeRouteRemote, Courier: "loopback", SSHHost: "h", SSHUser: "u"}
	if _, err := remote.SSHConfig(); err == nil {
		t.Fatal("non-SSH courier exposed SSH config")
	}
	remote.Courier = string(sessionmove.CourierSSH)
	remote.SSHHost = ""
	if _, err := remote.SSHConfig(); err == nil {
		t.Fatal("invalid SSH host exposed SSH config")
	}
	remote.SSHHost = "target.example"
	config, err := remote.SSHConfig()
	if err != nil || config != (sessionmove.SSHConfig{Host: "target.example", User: "u"}) {
		t.Fatalf("config=%#v err=%v", config, err)
	}
}

func TestSpCovLoadResumeRouteAtRejectsTamperedArtifacts(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	path := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceResumeRouteFileName)
	spCovWriteRaw(t, path, []byte("{not json"), 0o600)
	if _, _, err := loadResumeRouteAt(spCovOpenAggregate(t, store.Root, bundle.ParkedSessionID), bundle.ParkedSessionID); err == nil {
		t.Fatal("malformed route decoded")
	}
	invalid := ResumeRoute{SchemaVersion: SchemaVersion, ParkedSessionID: bundle.ParkedSessionID, Mode: "bogus", ClaimedAt: time.Unix(1, 0)}
	raw, err := jsonMarshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	spCovWriteRaw(t, path, raw, 0o600)
	if _, _, err := loadResumeRouteAt(spCovOpenAggregate(t, store.Root, bundle.ParkedSessionID), bundle.ParkedSessionID); err == nil {
		t.Fatal("invalid route decoded")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+"-outside", path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadResumeRouteAt(spCovOpenAggregate(t, store.Root, bundle.ParkedSessionID), bundle.ParkedSessionID); err == nil {
		t.Fatal("symlinked route opened")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, found, err := loadResumeRouteAt(spCovOpenAggregate(t, store.Root, bundle.ParkedSessionID), bundle.ParkedSessionID); err != nil || found {
		t.Fatalf("absent route found=%t err=%v", found, err)
	}
}

func spCovOpenAggregate(t *testing.T, root, parkID string) *os.File {
	t.Helper()
	dir, err := os.Open(spCovAggregatePath(root, parkID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return dir
}

func TestSpCovAppendSourceEventSequencingAndIdempotence(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	id := bundle.ParkedSessionID
	eventsDir := filepath.Join(spCovAggregatePath(store.Root, id), sourceEventsDirName)
	if err := os.RemoveAll(eventsDir); err != nil {
		t.Fatal(err)
	}
	if err := appendSourceEventAt(lock.aggregate, id, spCovResumedEvent(1, nil)); err == nil {
		t.Fatal("event appended without an events directory")
	}
	if err := os.Mkdir(eventsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rogue := filepath.Join(eventsDir, "rogue.json")
	spCovWriteRaw(t, rogue, []byte("{}\n"), 0o600)
	if err := appendSourceEventAt(lock.aggregate, id, spCovResumedEvent(1, nil)); err == nil {
		t.Fatal("event appended beside a rogue artifact")
	}
	if err := os.Remove(rogue); err != nil {
		t.Fatal(err)
	}
	if err := appendSourceEventAt(lock.aggregate, id, spCovResumedEvent(5, nil)); err == nil {
		t.Fatal("gapped event sequence accepted")
	}
	if err := appendSourceEventAt(lock.aggregate, id, spCovResumedEvent(1, nil)); err != nil {
		t.Fatal(err)
	}
	if err := appendSourceEventAt(lock.aggregate, id, spCovResumedEvent(2, nil)); err != nil {
		t.Fatal(err)
	}
	if err := appendSourceEventAt(lock.aggregate, id, spCovResumedEvent(4, nil)); err != nil {
		t.Fatalf("idempotent append after resume = %v", err)
	}
	entries, err := os.ReadDir(eventsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("event files = %v", entries)
	}
	state, err := store.Load(id)
	if err != nil || state.Status != StatusResumed || len(state.Events) != 2 || state.Successor == nil ||
		state.Successor.WBSessionID != "wbs-succ" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestSpCovListSourceEventsRejectsEveryMalformedArtifact(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	id := bundle.ParkedSessionID
	eventsDir := filepath.Join(spCovAggregatePath(store.Root, id), sourceEventsDirName)
	openEvents := func(t *testing.T) *os.File {
		t.Helper()
		dir, err := os.Open(eventsDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = dir.Close() })
		return dir
	}

	bundleFile, err := os.Open(filepath.Join(spCovAggregatePath(store.Root, id), sourceBundleFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listSourceEventsAt(bundleFile, id); err == nil {
		t.Fatal("regular file accepted as an events directory")
	}
	_ = bundleFile.Close()

	spCovWriteRaw(t, filepath.Join(eventsDir, "rogue.json"), []byte("{}\n"), 0o600)
	if _, err := listSourceEventsAt(openEvents(t), id); err == nil {
		t.Fatal("rogue artifact accepted")
	}
	if err := os.Remove(filepath.Join(eventsDir, "rogue.json")); err != nil {
		t.Fatal(err)
	}

	eventName := filepath.Join(eventsDir, "00000000000000000001.json")
	if err := os.Mkdir(eventName, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := listSourceEventsAt(openEvents(t), id); err == nil {
		t.Fatal("directory named like an event accepted")
	}
	if err := os.Remove(eventName); err != nil {
		t.Fatal(err)
	}

	spCovWriteRaw(t, eventName, []byte("{}\n"), 0o600)
	if _, err := listSourceEventsAt(openEvents(t), id); err == nil {
		t.Fatal("malformed event accepted")
	}
	spCovWriteRaw(t, eventName, []byte("{}\n"), 0o600)
	if err := os.Chmod(eventName, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := listSourceEventsAt(openEvents(t), id); err == nil {
		t.Fatal("non-private event artifact accepted")
	}
	if err := os.Chmod(eventName, 0o600); err != nil {
		t.Fatal(err)
	}

	remote := spCovResumedEvent(1, func(event *Event) {
		event.RemoteResumeID = "resume-x"
		event.TargetMachine = ".."
		event.RequestDigest = sessionmove.Digest("sha256:" + strings.Repeat("0", 64))
	})
	raw, err := jsonMarshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	spCovWriteRaw(t, eventName, raw, 0o600)
	if _, err := listSourceEventsAt(openEvents(t), id); err == nil {
		t.Fatal("invalid remote event accepted")
	}

	valid := spCovResumedEvent(1, nil)
	raw, err = jsonMarshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	spCovWriteRaw(t, eventName, raw, 0o600)
	events, err := listSourceEventsAt(openEvents(t), id)
	if err != nil || len(events) != 1 || events[0].Successor == nil || events[0].Successor.WBSessionID != "wbs-succ" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestSpCovLoadSourceStateRejectsCorruptEventEvidence(t *testing.T) {
	t.Run("events directory missing", func(t *testing.T) {
		store, bundle := spCovCreatedStore(t)
		if err := os.RemoveAll(filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceEventsDirName)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(bundle.ParkedSessionID); err == nil {
			t.Fatal("state loaded without an events directory")
		}
	})
	t.Run("rogue event artifact", func(t *testing.T) {
		store, bundle := spCovCreatedStore(t)
		eventsDir := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceEventsDirName)
		spCovWriteRaw(t, filepath.Join(eventsDir, "rogue.json"), []byte("{}\n"), 0o600)
		if _, err := store.Load(bundle.ParkedSessionID); err == nil {
			t.Fatal("state loaded with a rogue event artifact")
		}
	})
	t.Run("corrupt resume route", func(t *testing.T) {
		store, bundle := spCovCreatedStore(t)
		spCovWriteRaw(t, filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceResumeRouteFileName), []byte("{}\n"), 0o600)
		if _, err := store.Load(bundle.ParkedSessionID); err == nil {
			t.Fatal("state loaded with a corrupt route")
		}
	})
	t.Run("remote receipt absent and conflicting", func(t *testing.T) {
		store, bundle := spCovCreatedStore(t)
		eventsDir := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceEventsDirName)
		remote := spCovResumedEvent(1, func(event *Event) {
			event.RemoteResumeID = "resume-x"
			event.TargetMachine = "target"
			event.RequestDigest = sessionmove.Digest("sha256:" + strings.Repeat("0", 64))
		})
		spCovWriteEvent(t, eventsDir, remote)
		if _, err := store.Load(bundle.ParkedSessionID); err == nil || !strings.Contains(err.Error(), "load source remote receipt") {
			t.Fatalf("missing receipt error = %v", err)
		}
		conflicting := spCovReceiptFixture("resume-other", remote.RequestDigest)
		raw, err := EncodeReceipt(conflicting)
		if err != nil {
			t.Fatal(err)
		}
		spCovWriteRaw(t, filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceReceiptName("target")), raw, 0o600)
		if _, err := store.Load(bundle.ParkedSessionID); err == nil || !strings.Contains(err.Error(), "conflicts with resumed event") {
			t.Fatalf("conflicting receipt error = %v", err)
		}
		matching := spCovReceiptFixture("resume-x", remote.RequestDigest)
		raw, err = EncodeReceipt(matching)
		if err != nil {
			t.Fatal(err)
		}
		spCovWriteRaw(t, filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceReceiptName("target")), raw, 0o600)
		state, err := store.Load(bundle.ParkedSessionID)
		if err != nil || state.RemoteReceipt == nil || state.RemoteReceipt.ResumeID != "resume-x" {
			t.Fatalf("state=%#v err=%v", state, err)
		}
	})
}

func TestSpCovPrepareRemoteUnderLockRejectsUnsafeRoutes(t *testing.T) {
	store := NewStore(t.TempDir())
	bundle := remoteTestBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	ssh := testParkedSSH()
	courier := string(sessionmove.CourierSSH)
	if _, err := store.PrepareRemoteUnderLock(nil, "target", "", courier, ssh, time.Now()); err == nil {
		t.Fatal("nil lock prepared a remote route")
	}
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	for name, call := range map[string]func() (RemoteAdmission, error){
		"invalid target": func() (RemoteAdmission, error) {
			return store.PrepareRemoteUnderLock(lock, "..", "", courier, ssh, time.Now())
		},
		"wrong courier": func() (RemoteAdmission, error) {
			return store.PrepareRemoteUnderLock(lock, "target", "", "loopback", ssh, time.Now())
		},
		"custom path": func() (RemoteAdmission, error) {
			return store.PrepareRemoteUnderLock(lock, "target", "", courier, sessionmove.SSHConfig{Host: "target.example", WBPath: "/opt/wb"}, time.Now())
		},
		"invalid ssh": func() (RemoteAdmission, error) {
			return store.PrepareRemoteUnderLock(lock, "target", "", courier, sessionmove.SSHConfig{}, time.Now())
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := call(); err == nil {
				t.Fatal("unsafe remote route prepared")
			}
		})
	}
	first, err := store.PrepareRemoteUnderLock(lock, "target", "", courier, ssh, time.Time{})
	if err != nil || first.Replay || first.Envelope.Request.CreatedAt.IsZero() {
		t.Fatalf("admission=%#v err=%v", first, err)
	}
	if _, err := store.PrepareRemoteUnderLock(lock, "target", "claude-code", courier, ssh, time.Now()); err == nil {
		t.Fatal("conflicting requested harness reused the durable envelope")
	}
	envelopePath := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceEnvelopeName("target"))
	if err := os.Remove(envelopePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(envelopePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareRemoteUnderLock(lock, "target", "", courier, ssh, time.Now()); err == nil {
		t.Fatal("unreadable durable envelope accepted")
	}
}

func TestSpCovPrepareRemoteUnderLockRefusesResumedSource(t *testing.T) {
	store := NewStore(t.TempDir())
	bundle := remoteTestBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	admission, err := store.PrepareRemoteUnderLock(lock, "target", "", string(sessionmove.CourierSSH), testParkedSSH(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRemoteReceiptUnderLock(lock, admission, validRemoteReceipt(t, admission)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeRemoteUnderLock(lock, admission, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareRemoteUnderLock(lock, "target", "", string(sessionmove.CourierSSH), testParkedSSH(), time.Now()); err == nil {
		t.Fatal("resumed source prepared a second remote admission")
	}
}

func TestSpCovValidateRemoteAdmissionRequiresExactDurableBytes(t *testing.T) {
	store := NewStore(t.TempDir())
	bundle := remoteTestBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	admission, err := store.PrepareRemoteUnderLock(lock, "target", "", string(sessionmove.CourierSSH), testParkedSSH(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.validateRemoteAdmission(nil, admission); err == nil {
		t.Fatal("nil lock validated an admission")
	}
	if err := store.validateRemoteAdmission(lock, admission); err != nil {
		t.Fatalf("valid admission rejected: %v", err)
	}

	routeMismatch := admission
	routeMismatch.Route.TargetMachine = "other-target"
	if err := store.validateRemoteAdmission(lock, routeMismatch); err == nil || !strings.Contains(err.Error(), "exact durable courier route") {
		t.Fatalf("route mismatch error = %v", err)
	}
	rawMismatch := admission
	rawMismatch.Raw = append(bytes.Clone(admission.Raw), ' ')
	if err := store.validateRemoteAdmission(lock, rawMismatch); err == nil || !strings.Contains(err.Error(), "exact durable envelope") {
		t.Fatalf("raw mismatch error = %v", err)
	}
	digestMismatch := admission
	digestMismatch.Digest = sessionmove.Digest("sha256:" + strings.Repeat("0", 64))
	if err := store.validateRemoteAdmission(lock, digestMismatch); err == nil || !strings.Contains(err.Error(), "exact durable envelope") {
		t.Fatalf("digest mismatch error = %v", err)
	}
	parkMismatch := admission
	parkMismatch.Envelope.Request.ParkedSessionID = "park-other"
	reEncoded, err := EncodeEnvelope(parkMismatch.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	parkMismatch.Raw = reEncoded
	parkMismatch.Digest = sessionmove.DigestBytes(reEncoded)
	if err := store.validateRemoteAdmission(lock, parkMismatch); err == nil || !strings.Contains(err.Error(), "exact durable envelope") {
		t.Fatalf("parked ID mismatch error = %v", err)
	}

	envelopePath := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceEnvelopeName("target"))
	spCovWriteRaw(t, envelopePath, []byte("{}\n"), 0o600)
	if err := store.validateRemoteAdmission(lock, admission); err == nil || !strings.Contains(err.Error(), "changed after durable admission") {
		t.Fatalf("changed envelope error = %v", err)
	}
}

func TestSpCovLoadAndSaveRemoteReceiptUnderLock(t *testing.T) {
	store := NewStore(t.TempDir())
	bundle := remoteTestBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	admission, err := store.PrepareRemoteUnderLock(lock, "target", "", string(sessionmove.CourierSSH), testParkedSSH(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRemoteReceiptUnderLock(nil, admission); err == nil {
		t.Fatal("nil lock loaded a receipt")
	}
	if receipt, err := store.LoadRemoteReceiptUnderLock(lock, admission); err != nil || receipt != nil {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	if err := store.SaveRemoteReceiptUnderLock(nil, admission, validRemoteReceipt(t, admission)); err == nil {
		t.Fatal("nil lock saved a receipt")
	}
	if err := store.SaveRemoteReceiptUnderLock(lock, admission, spCovReceiptFixture("resume-other", admission.Digest)); err == nil {
		t.Fatal("conflicting receipt saved")
	}

	receiptPath := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceReceiptName("target"))
	spCovWriteRaw(t, receiptPath, []byte("{}\n"), 0o600)
	if _, err := store.LoadRemoteReceiptUnderLock(lock, admission); err == nil {
		t.Fatal("malformed receipt loaded")
	}
	conflicting := spCovReceiptFixture("resume-other", admission.Digest)
	raw, err := EncodeReceipt(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	spCovWriteRaw(t, receiptPath, raw, 0o600)
	if _, err := store.LoadRemoteReceiptUnderLock(lock, admission); err == nil {
		t.Fatal("identity-conflicting receipt loaded")
	}
	if err := os.Remove(receiptPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(receiptPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRemoteReceiptUnderLock(lock, admission); err == nil {
		t.Fatal("directory accepted as a receipt")
	}
	if err := os.Remove(receiptPath); err != nil {
		t.Fatal(err)
	}

	good := validRemoteReceipt(t, admission)
	if err := store.SaveRemoteReceiptUnderLock(lock, admission, good); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadRemoteReceiptUnderLock(lock, admission)
	if err != nil || loaded == nil || loaded.ResumeID != good.ResumeID || loaded.AttemptID != good.AttemptID {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestSpCovFinalizeRemoteUnderLockRequiresDurableReceipt(t *testing.T) {
	store := NewStore(t.TempDir())
	bundle := remoteTestBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	admission, err := store.PrepareRemoteUnderLock(lock, "target", "", string(sessionmove.CourierSSH), testParkedSSH(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeRemoteUnderLock(nil, admission, time.Now()); err == nil {
		t.Fatal("nil lock finalized a remote resume")
	}
	if _, err := store.FinalizeRemoteUnderLock(lock, admission, time.Now()); err == nil || !strings.Contains(err.Error(), "no durable validated receipt") {
		t.Fatalf("missing receipt error = %v", err)
	}
	receipt := validRemoteReceipt(t, admission)
	if err := store.SaveRemoteReceiptUnderLock(lock, admission, receipt); err != nil {
		t.Fatal(err)
	}
	state, err := store.FinalizeRemoteUnderLock(lock, admission, time.Unix(200, 0))
	if err != nil || state.Status != StatusResumed || state.RemoteReceipt == nil || state.Successor == nil ||
		state.Successor.WBSessionID != receipt.SuccessorWBSessionID || state.Events[0].TargetMachine != "target" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	again, err := store.FinalizeRemoteUnderLock(lock, admission, time.Unix(300, 0))
	if err != nil || again.Successor == nil || len(again.Events) != 1 {
		t.Fatalf("retry state=%#v err=%v", again, err)
	}
}

func TestSpCovSourceAcquireRejectsUnsafeAggregates(t *testing.T) {
	if _, err := NewStore(t.TempDir()).Acquire(context.Background(), ".."); err == nil {
		t.Fatal("invalid park ID acquired")
	}
	if _, err := NewStore("relative-store").Acquire(context.Background(), "park-test"); err == nil {
		t.Fatal("relative store root acquired")
	}
	if _, err := NewStore(filepath.Join(t.TempDir(), "missing-store")).Acquire(context.Background(), "park-test"); err == nil {
		t.Fatal("missing store root acquired")
	}
	if _, err := NewStore(t.TempDir()).Acquire(context.Background(), "park-test"); err == nil {
		t.Fatal("missing aggregate acquired")
	}

	for name, mutate := range map[string]func(t *testing.T, store Store, bundle Bundle){
		"bundle missing": func(t *testing.T, store Store, bundle Bundle) {
			if err := os.Remove(filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceBundleFileName)); err != nil {
				t.Fatal(err)
			}
		},
		"bundle not private": func(t *testing.T, store Store, bundle Bundle) {
			if err := os.Chmod(filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceBundleFileName), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"bundle malformed": func(t *testing.T, store Store, bundle Bundle) {
			spCovWriteRaw(t, filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceBundleFileName), []byte("{}\n"), 0o600)
		},
		"bundle noncanonical": func(t *testing.T, store Store, bundle Bundle) {
			raw, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			spCovWriteRaw(t, filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceBundleFileName), raw, 0o600)
		},
		"lock symlink": func(t *testing.T, store Store, bundle Bundle) {
			path := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceResumeLockName)
			if err := os.Symlink(filepath.Join(t.TempDir(), "outside-lock"), path); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, bundle := spCovCreatedStore(t)
			mutate(t, store, bundle)
			if _, err := store.Acquire(context.Background(), bundle.ParkedSessionID); err == nil {
				t.Fatal("unsafe aggregate acquired")
			}
		})
	}

	t.Run("aggregate ID mismatch", func(t *testing.T) {
		store, bundle := spCovCreatedStore(t)
		agg := spCovAggregatePath(store.Root, bundle.ParkedSessionID)
		renamed := spCovAggregatePath(store.Root, "park-renamed")
		if err := os.Rename(agg, renamed); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Acquire(context.Background(), "park-renamed"); err == nil {
			t.Fatal("aggregate with mismatched bundle identity acquired")
		}
	})
}

func TestSpCovSourceAcquireFenceHonoursCancelledContext(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	held := spCovAcquire(t, store, bundle.ParkedSessionID)
	// An empty digest is the documented wildcard used by held(): it authenticates
	// the retained aggregate without rebinding it to one exact byte sequence.
	if !held.HeldForSession(store.Root, bundle.ParkedSessionID, "") {
		t.Fatal("empty digest wildcard did not authenticate")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Acquire(ctx, bundle.ParkedSessionID); err == nil {
		t.Fatal("contending acquire ignored the held fence")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("contending acquire error = %v", err)
	}
}
