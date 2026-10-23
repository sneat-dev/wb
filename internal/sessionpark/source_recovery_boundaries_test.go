package sessionpark

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestSourcePublicationRetainsTimestampEncodingRefusals(t *testing.T) {
	t.Parallel()
	t.Run("local route", func(t *testing.T) {
		t.Parallel()
		store, bundle := spCovCreatedStore(t)
		lock := spCovAcquire(t, store, bundle.ParkedSessionID)
		_, _, err := store.PrepareLocalUnderLock(lock, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
		if err == nil || !strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
			t.Fatalf("route = %v", err)
		}
		if _, err := os.Stat(filepath.Join(store.Root, bundle.ParkedSessionID, sourceResumeRouteFileName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid route published: %v", err)
		}
	})
	t.Run("local resumed event", func(t *testing.T) {
		t.Parallel()
		store, bundle := spCovCreatedStore(t)
		lock := spCovAcquire(t, store, bundle.ParkedSessionID)
		if _, _, err := store.PrepareLocalUnderLock(lock, time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
		_, err := store.ResumeUnderLock(lock, session.Record{PID: 42, WBSessionID: "wbs-successor"}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
		if err == nil || !strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
			t.Fatalf("resume = %v", err)
		}
		state, err := store.LoadUnderLock(lock)
		if err != nil || state.Status != StatusParked || len(state.Events) != 0 {
			t.Fatalf("failed publication changed state: %#v, %v", state, err)
		}
	})
	t.Run("remote receipt", func(t *testing.T) {
		t.Parallel()
		store, bundle := parkedRemoteStoreForTest(t)
		lock := spCovAcquire(t, store, bundle.ParkedSessionID)
		admission, err := store.PrepareRemoteUnderLock(lock, "target", "", string(sessionmove.CourierSSH), testParkedSSH(), time.Unix(100, 0))
		if err != nil {
			t.Fatal(err)
		}
		receipt := validRemoteReceipt(t, admission)
		receipt.StartedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := store.SaveRemoteReceiptUnderLock(lock, admission, receipt); err == nil || !strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
			t.Fatalf("receipt = %v", err)
		}
	})
	t.Run("remote finalization", func(t *testing.T) {
		t.Parallel()
		store, bundle := parkedRemoteStoreForTest(t)
		lock := spCovAcquire(t, store, bundle.ParkedSessionID)
		admission, err := store.PrepareRemoteUnderLock(lock, "target", "", string(sessionmove.CourierSSH), testParkedSSH(), time.Unix(100, 0))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveRemoteReceiptUnderLock(lock, admission, validRemoteReceipt(t, admission)); err != nil {
			t.Fatal(err)
		}
		_, err = store.FinalizeRemoteUnderLock(lock, admission, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
		if err == nil || !strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
			t.Fatalf("finalize = %v", err)
		}
	})
}

func TestSourceStateRefusesConflictingRemoteReceipt(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			store, bundle := parkedRemoteStoreForTest(t)
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
			path := filepath.Join(store.Root, bundle.ParkedSessionID, sourceReceiptName("target"))
			if mode == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.LoadUnderLock(lock); err == nil {
				t.Fatal("unverifiable remote receipt accepted")
			}
		})
	}
}

func TestStoreRootParentSyncFailureClosesOwnedDescriptors(t *testing.T) {
	t.Parallel()
	refused := errors.New("parent sync refused")
	var parent *os.File
	f, err := openPrivateStoreRootWithInjector(filepath.Join(t.TempDir(), "store"), true, nil, func(p *os.File) error { parent = p; return refused })
	if f != nil || !errors.Is(err, refused) {
		t.Fatalf("root = %v, %v", f, err)
	}
	if _, err := parent.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("parent retained after failed sync: %v", err)
	}
}

func parkedRemoteStoreForTest(t *testing.T) (Store, Bundle) {
	t.Helper()
	store := NewStore(t.TempDir())
	bundle := remoteTestBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	return store, bundle
}

func TestRemotePreparationRefusesInvalidEnvelopeWithoutPublishingIt(t *testing.T) {
	t.Parallel()
	store, bundle := parkedRemoteStoreForTest(t)
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	admission, err := store.PrepareRemoteUnderLock(lock, "target", "codex\ninvalid", string(sessionmove.CourierSSH), testParkedSSH(), time.Unix(100, 0))
	if err == nil || admission.Digest != "" {
		t.Fatalf("prepare = %#v, %v", admission, err)
	}
	path := filepath.Join(store.Root, bundle.ParkedSessionID, sourceEnvelopeName("target"))
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid envelope published: %v", err)
	}
}

func TestSourcePublicationRetainsWriteRefusals(t *testing.T) {
	t.Parallel()
	t.Run("route", func(t *testing.T) {
		t.Parallel()
		store, bundle := spCovCreatedStore(t)
		lock := spCovAcquire(t, store, bundle.ParkedSessionID)
		refused := errors.New("route write refused")
		_, _, err := store.claimResumeRouteWithInjector(lock, ResumeRouteLocal, "", "", sessionmove.SSHConfig{}, time.Unix(100, 0), &filewrite.Injector{Step: filewrite.StepWrite, Name: sourceResumeRouteFileName, Err: refused})
		if !errors.Is(err, refused) {
			t.Fatalf("route = %v", err)
		}
	})
	t.Run("envelope", func(t *testing.T) {
		t.Parallel()
		store, bundle := parkedRemoteStoreForTest(t)
		lock := spCovAcquire(t, store, bundle.ParkedSessionID)
		refused := errors.New("envelope write refused")
		_, err := store.prepareRemoteUnderLock(lock, "target", "", string(sessionmove.CourierSSH), testParkedSSH(), time.Unix(100, 0), &filewrite.Injector{Step: filewrite.StepWrite, Name: sourceEnvelopeName("target"), Err: refused})
		if !errors.Is(err, refused) {
			t.Fatalf("envelope = %v", err)
		}
	})
	t.Run("neutral mkdir", func(t *testing.T) {
		t.Parallel()
		bundle := testBundle(t)
		bundle.Worktrees = nil
		store := NewStore(t.TempDir())
		if _, err := store.Create(bundle); err != nil {
			t.Fatal(err)
		}
		lock := spCovAcquire(t, store, bundle.ParkedSessionID)
		if _, _, err := store.PrepareLocalUnderLock(lock, time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
		refused := errors.New("neutral mkdir refused")
		_, err := store.localLaunchRootUnderLock(lock, nil, func(fd int, name string, mode uint32) error { return refused })
		if !errors.Is(err, refused) {
			t.Fatalf("neutral = %v", err)
		}
	})
}

func TestSourceEventPublicationRefusesFailureAndCompetingArtifact(t *testing.T) {
	t.Parallel()
	for _, competing := range []bool{false, true} {
		t.Run(fmt.Sprint(competing), func(t *testing.T) {
			t.Parallel()
			store, bundle := spCovCreatedStore(t)
			lock := spCovAcquire(t, store, bundle.ParkedSessionID)
			event := spCovResumedEvent(1, nil)
			refused := errors.New("event publication refused")
			inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: refused}
			if competing {
				inj.Err = nil
				inj.Hook = func() {
					if err := os.WriteFile(filepath.Join(store.Root, bundle.ParkedSessionID, sourceEventsDirName, "00000000000000000001.json"), []byte("occupied\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := appendSourceEventAtInjected(lock.aggregate, bundle.ParkedSessionID, event, inj)
			if competing {
				if err == nil || !strings.Contains(err.Error(), "unknown bytes") {
					t.Fatalf("competing event = %v", err)
				}
			} else if !errors.Is(err, refused) {
				t.Fatalf("failed event = %v", err)
			}
		})
	}
}

func TestLocalSuccessorContextUsesResolvedMemberPath(t *testing.T) {
	t.Parallel()
	store, bundle := spCovCreatedStore(t)
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	if _, _, err := store.PrepareLocalUnderLock(lock, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	resolved := filepath.Join(t.TempDir(), "relocated")
	_, raw, err := store.EnsureLocalSuccessorContextUnderLock(lock, map[string]string{bundle.Worktrees[0].WorktreeDir: resolved})
	if err != nil || !strings.Contains(string(raw), "path: "+resolved+"\n") {
		t.Fatalf("resolved context = %q, %v", raw, err)
	}
}

func TestPrivateStoreRootRefusesOversizedBasename(t *testing.T) {
	t.Parallel()
	f, err := openPrivateStoreRoot(filepath.Join(t.TempDir(), strings.Repeat("a", 1024)), true)
	if err == nil || f != nil {
		t.Fatalf("oversized root = %v, %v", f, err)
	}
}
