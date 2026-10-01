package worktrees

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCleanupMemberRecheckProofBoundaries(t *testing.T) {
	t.Parallel()
	denied := errors.New("denied")
	for _, tc := range []struct {
		name      string
		preflight bool
		change    func(*cleanupMemberRecheckPorts)
		want      string
	}{
		{name: "preflight success", preflight: true},
		{name: "removal success"},
		{name: "initial descriptor", change: func(p *cleanupMemberRecheckPorts) { p.Validate = func() error { return denied } }, want: "denied"},
		{name: "preflight inspection", preflight: true, change: func(p *cleanupMemberRecheckPorts) {
			p.Inspect = func(context.Context, cleanupMemberRecheck) (ListResult, error) { return ListResult{}, denied }
		}, want: "preflight cleanup acme/app: denied"},
		{name: "removal inspection", change: func(p *cleanupMemberRecheckPorts) {
			p.Inspect = func(context.Context, cleanupMemberRecheck) (ListResult, error) { return ListResult{}, denied }
		}, want: "denied"},
		{name: "preflight merge proof", preflight: true, change: func(p *cleanupMemberRecheckPorts) {
			p.MergeProof = func(context.Context, CleanupOptions, *ListResult) error { return denied }
		}, want: "preflight cleanup acme/app receipt proof: denied"},
		{name: "removal merge proof", change: func(p *cleanupMemberRecheckPorts) {
			p.MergeProof = func(context.Context, CleanupOptions, *ListResult) error { return denied }
		}, want: "cleanup receipt proof for acme/app: denied"},
		{name: "preflight acknowledgement", preflight: true, change: func(p *cleanupMemberRecheckPorts) {
			p.Acknowledgement = func(context.Context, string, *ListResult) error { return denied }
		}, want: "preflight cleanup acme/app absorbed-conflict acknowledgement proof: denied"},
		{name: "removal acknowledgement", change: func(p *cleanupMemberRecheckPorts) {
			p.Acknowledgement = func(context.Context, string, *ListResult) error { return denied }
		}, want: "cleanup absorbed-conflict acknowledgement proof for acme/app: denied"},
		{name: "preflight supersession refusal", preflight: true, change: func(p *cleanupMemberRecheckPorts) {
			p.Supersession = func(_ context.Context, _ string, result *ListResult) { result.SupersessionRejection = "bad receipt" }
		}, want: "preflight cleanup acme/app supersession receipt refused: bad receipt"},
		{name: "second descriptor", change: func(p *cleanupMemberRecheckPorts) {
			calls := 0
			p.Validate = func() error {
				calls++
				if calls == 2 {
					return denied
				}
				return nil
			}
		}, want: "denied"},
		{name: "ineligible", change: func(p *cleanupMemberRecheckPorts) {
			p.Eligible = func(ListResult, CleanupOptions, time.Time) (bool, string) { return false, "dirty" }
		}, want: "cleanup safety changed for acme/app: dirty"},
		{name: "head moved", change: func(p *cleanupMemberRecheckPorts) {
			p.Inspect = func(context.Context, cleanupMemberRecheck) (ListResult, error) {
				return ListResult{Repository: "acme/app", HeadSHA: "other"}, nil
			}
		}, want: "cleanup safety changed for acme/app: branch head moved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ports := cleanupMemberRecheckPorts{
				Validate: func() error { return nil },
				Inspect: func(context.Context, cleanupMemberRecheck) (ListResult, error) {
					return ListResult{Repository: "acme/app", HeadSHA: "head"}, nil
				},
				MergeProof:      func(context.Context, CleanupOptions, *ListResult) error { return nil },
				Acknowledgement: func(context.Context, string, *ListResult) error { return nil },
				Supersession:    func(context.Context, string, *ListResult) {},
				Eligible:        func(ListResult, CleanupOptions, time.Time) (bool, string) { return true, "" },
			}
			if tc.change != nil {
				tc.change(&ports)
			}
			result, err := recheckCleanupMember(context.Background(), cleanupMemberRecheck{
				Entry:     CleanupResult{ListResult: ListResult{Repository: "acme/app", HeadSHA: "head"}},
				Preflight: tc.preflight,
			}, ports)
			if tc.want == "" {
				if err != nil || result.HeadSHA != "head" {
					t.Fatalf("recheck result=%#v err=%v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("recheck result=%#v err=%v, want %q", result, err, tc.want)
			}
		})
	}
}

func TestCleanupBacklogResumeFaultMatrix(t *testing.T) {
	t.Parallel()
	denied := errors.New("denied")
	base := lifecycleBacklogRecord{
		ID: "record", Task: "task", Repository: "acme/app", ProjectsRoot: "/projects",
		CanonicalDir: "/projects/acme/app", WorktreesRoot: "/projects/acme/app/.worktrees",
		WorktreeDir: "/projects/acme/app/.worktrees/task", Branch: "topic", Base: "main",
		HeadSHA: "abc", RemoteHeadSHA: "abc", Local: true, Stage: lifecycleStageRemovingWorktree,
	}
	for _, tc := range []struct {
		name   string
		change func(*lifecycleBacklogRecord, *cleanupBacklogPorts)
		want   string
	}{
		{name: "already retired"},
		{name: "record invalid", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.ValidateRecord = func(lifecycleBacklogRecord) error { return denied }
		}, want: "denied"},
		{name: "live task lock", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.AcquireTask = func(string, string, bool) (*cleanupTaskHandle, error) { return nil, denied }
		}, want: "lock lifecycle backlog task task: denied"},
		{name: "vacant task", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.AcquireTask = func(string, string, bool) (*cleanupTaskHandle, error) { return nil, os.ErrNotExist }
			p.CompleteVacant = func(context.Context, string, *lifecycleBacklogRecord, error) error { return nil }
		}},
		{name: "vacant task unfinished", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.AcquireTask = func(string, string, bool) (*cleanupTaskHandle, error) { return nil, os.ErrNotExist }
			p.CompleteVacant = func(context.Context, string, *lifecycleBacklogRecord, error) error { return denied }
		}, want: "denied"},
		{name: "open canonical", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.OpenCanonical = func(string) (*canonicalRepository, error) { return nil, denied }
		}, want: "denied"},
		{name: "canonical changed", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.ValidateCanonical = func(*canonicalRepository) error { return denied }
		}, want: "denied"},
		{name: "remote read", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.RemoteHead = func(context.Context, *lifecycleBacklogRecord) (string, error) { return "", denied }
		}, want: "denied"},
		{name: "remote authority", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.RemoteHead = func(context.Context, *lifecycleBacklogRecord) (string, error) { return "abc", nil }
		}, want: "origin/topic still exists"},
		{name: "advanced remote", change: func(r *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			r.Stage = lifecycleStageRetiringRemote
			p.RemoteHead = func(context.Context, *lifecycleBacklogRecord) (string, error) { return "advanced", nil }
		}, want: "advanced from abc to advanced"},
		{name: "missing recorded remote SHA", change: func(r *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			r.Stage = lifecycleStageRetiringRemote
			r.RemoteHeadSHA = ""
			p.RemoteHead = func(context.Context, *lifecycleBacklogRecord) (string, error) { return "abc", nil }
		}, want: "advanced from  to abc"},
		{name: "exact remote lease", change: func(r *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			r.Stage = lifecycleStageRetiringRemote
			p.RemoteHead = func(context.Context, *lifecycleBacklogRecord) (string, error) { return "abc", nil }
		}},
		{name: "remote push", change: func(r *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			r.Stage = lifecycleStageRetiringRemote
			p.RemoteHead = func(context.Context, *lifecycleBacklogRecord) (string, error) { return "abc", nil }
			p.DeleteRemote = func(context.Context, *canonicalRepository, *lifecycleBacklogRecord) error { return denied }
		}, want: "resume exact remote branch retirement topic: denied"},
		{name: "remote journal", change: func(r *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			r.Stage = lifecycleStageRetiringRemote
			p.RemoteHead = func(context.Context, *lifecycleBacklogRecord) (string, error) { return "abc", nil }
			p.Persist = func(_ string, _ *lifecycleBacklogRecord, stage string) error {
				if stage == lifecycleStageRemoteRetired {
					return denied
				}
				return nil
			}
		}, want: "denied"},
		{name: "registration read", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.Registrations = func(context.Context, *canonicalRepository) (map[string]bool, error) { return nil, denied }
		}, want: "denied"},
		{name: "still registered", change: func(r *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.Registrations = func(context.Context, *canonicalRepository) (map[string]bool, error) {
				return map[string]bool{r.WorktreeDir: true}, nil
			}
		}, want: "worktree remains registered"},
		{name: "path read", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.Lstat = func(string) (os.FileInfo, error) { return nil, denied }
		}, want: "denied"},
		{name: "residue open", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.Lstat = func(string) (os.FileInfo, error) { return nil, nil }
			p.OpenWorktree = func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error) { return nil, denied }
		}, want: "denied"},
		{name: "residue removal", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.Lstat = func(string) (os.FileInfo, error) { return nil, nil }
			p.RemoveResidue = func(*cleanupWorktreeHandle, string) (bool, error) { return false, denied }
		}, want: "denied"},
		{name: "residue parent", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.Lstat = func(string) (os.FileInfo, error) { return nil, nil }
			p.RemoveParent = func(*cleanupWorktreeHandle) error { return denied }
		}, want: "denied"},
		{name: "residue success", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.Lstat = func(string) (os.FileInfo, error) { return nil, nil }
		}},
		{name: "local branch read", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.LocalExists = func(context.Context, *canonicalRepository, string) (bool, error) { return false, denied }
		}, want: "denied"},
		{name: "local head read", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.LocalExists = func(context.Context, *canonicalRepository, string) (bool, error) { return true, nil }
			p.LocalHead = func(context.Context, *canonicalRepository, string) (string, error) { return "", denied }
		}, want: "denied"},
		{name: "local head changed", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.LocalExists = func(context.Context, *canonicalRepository, string) (bool, error) { return true, nil }
			p.LocalHead = func(context.Context, *canonicalRepository, string) (string, error) { return "advanced", nil }
		}, want: "branch moved from abc to advanced"},
		{name: "local journal", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.LocalExists = func(context.Context, *canonicalRepository, string) (bool, error) { return true, nil }
			p.Persist = func(_ string, _ *lifecycleBacklogRecord, stage string) error {
				if stage == lifecycleStageRemovingLocalBranch {
					return denied
				}
				return nil
			}
		}, want: "denied"},
		{name: "local deletion", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.LocalExists = func(context.Context, *canonicalRepository, string) (bool, error) { return true, nil }
			p.DeleteLocal = func(context.Context, *canonicalRepository, *lifecycleBacklogRecord) error { return denied }
		}, want: "resume exact local branch deletion topic: denied"},
		{name: "local exact delete", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.LocalExists = func(context.Context, *canonicalRepository, string) (bool, error) { return true, nil }
		}},
		{name: "preserve local branch", change: func(r *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			r.PreserveLocalBranch = true
			p.LocalExists = func(context.Context, *canonicalRepository, string) (bool, error) { return true, nil }
			p.RemoteHead = func(context.Context, *lifecycleBacklogRecord) (string, error) { return "abc", nil }
		}},
		{name: "detached", change: func(r *lifecycleBacklogRecord, _ *cleanupBacklogPorts) {
			r.Detached = true
			r.Branch = ""
			r.RemoteHeadSHA = ""
		}},
		{name: "seal create failure", change: func(r *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			r.RecoveryKind = "create_work_log_failed"
			r.WorkLogClaim = "claim"
			p.SealClaim = func(string, lifecycleBacklogRecord) error { return denied }
		}, want: "denied"},
		{name: "seal create failure success", change: func(r *lifecycleBacklogRecord, _ *cleanupBacklogPorts) {
			r.RecoveryKind = "create_work_log_failed"
			r.WorkLogClaim = "claim"
		}},
		{name: "complete journal", change: func(_ *lifecycleBacklogRecord, p *cleanupBacklogPorts) {
			p.Persist = func(_ string, _ *lifecycleBacklogRecord, stage string) error {
				if stage == lifecycleStageComplete {
					return denied
				}
				return nil
			}
		}, want: "denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := base
			ports := cleanupBacklogPorts{
				ValidateRecord:    func(lifecycleBacklogRecord) error { return nil },
				AcquireTask:       func(string, string, bool) (*cleanupTaskHandle, error) { return &cleanupTaskHandle{}, nil },
				ReleaseTask:       func(*cleanupTaskHandle) {},
				CompleteVacant:    func(context.Context, string, *lifecycleBacklogRecord, error) error { return nil },
				OpenCanonical:     func(string) (*canonicalRepository, error) { return &canonicalRepository{}, nil },
				ValidateCanonical: func(*canonicalRepository) error { return nil },
				CloseCanonical:    func(*canonicalRepository) {},
				RemoteHead:        func(context.Context, *lifecycleBacklogRecord) (string, error) { return "", nil },
				DeleteRemote:      func(context.Context, *canonicalRepository, *lifecycleBacklogRecord) error { return nil },
				Persist:           func(_ string, record *lifecycleBacklogRecord, stage string) error { record.Stage = stage; return nil },
				Registrations:     func(context.Context, *canonicalRepository) (map[string]bool, error) { return nil, nil },
				Lstat:             func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
				OpenWorktree: func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error) {
					return &cleanupWorktreeHandle{}, nil
				},
				RemoveResidue: func(*cleanupWorktreeHandle, string) (bool, error) { return true, nil },
				RemoveParent:  func(*cleanupWorktreeHandle) error { return nil },
				CloseWorktree: func(*cleanupWorktreeHandle) {},
				LocalExists:   func(context.Context, *canonicalRepository, string) (bool, error) { return false, nil },
				LocalHead:     func(context.Context, *canonicalRepository, string) (string, error) { return "abc", nil },
				DeleteLocal:   func(context.Context, *canonicalRepository, *lifecycleBacklogRecord) error { return nil },
				SealClaim:     func(string, lifecycleBacklogRecord) error { return nil },
			}
			if tc.change != nil {
				tc.change(&record, &ports)
			}
			err := resumeLifecycleBacklogWithPorts(context.Background(), "/home", &record, true, ports)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("resume err=%v record=%#v", err, record)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("resume err=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestCleanupExactRefDeleteCommands(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		kind cleanupRefKind
		want []string
	}{
		{name: "remote", kind: cleanupRemoteRef, want: []string{"push", "--force-with-lease=refs/heads/topic:abc", "origin", ":refs/heads/topic"}},
		{name: "local", kind: cleanupLocalRef, want: []string{"update-ref", "-d", "refs/heads/topic", "abc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			if err := invokeCleanupExactRefDelete(tc.kind, "topic", "abc", func(args ...string) error {
				got = append([]string(nil), args...)
				return nil
			}); err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("exact ref delete args=%q err=%v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestCleanupPreflightPortsPreserveBoundaries(t *testing.T) {
	t.Parallel()
	denied := errors.New("denied")
	for _, tc := range []struct {
		name   string
		change func(*cleanupPreflightPorts)
		want   string
	}{
		{name: "success"},
		{name: "open worktree", change: func(p *cleanupPreflightPorts) {
			p.OpenWorktree = func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error) { return nil, denied }
		}, want: "denied"},
		{name: "read only recheck", change: func(p *cleanupPreflightPorts) {
			p.Recheck = func(context.Context, cleanupMemberRecheck, *cleanupWorktreeHandle) (ListResult, error) {
				return ListResult{}, denied
			}
		}, want: "denied"},
		{name: "open canonical", change: func(p *cleanupPreflightPorts) {
			p.OpenCanonical = func(string) (*canonicalRepository, error) { return nil, denied }
		}, want: "open cleanup canonical repository /repo: denied"},
		{name: "validate canonical", change: func(p *cleanupPreflightPorts) {
			p.ValidateCanonical = func(*canonicalRepository) error { return denied }
		}, want: "cleanup canonical repository changed during preflight: denied"},
		{name: "work log", change: func(p *cleanupPreflightPorts) {
			p.WorkLog = func(context.Context, string, string, ListResult) error { return denied }
		}, want: "preflight Work Log for acme/app: denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ports := cleanupPreflightPorts{
				OpenWorktree: func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error) {
					return &cleanupWorktreeHandle{}, nil
				},
				Recheck: func(_ context.Context, request cleanupMemberRecheck, _ *cleanupWorktreeHandle) (ListResult, error) {
					if !request.Preflight {
						t.Fatal("preflight recheck lost its strict mode")
					}
					return ListResult{Repository: "acme/app", CanonicalDir: "/repo"}, nil
				},
				OpenCanonical:     func(string) (*canonicalRepository, error) { return &canonicalRepository{}, nil },
				ValidateCanonical: func(*canonicalRepository) error { return nil },
				WorkLog:           func(context.Context, string, string, ListResult) error { return nil },
			}
			if tc.change != nil {
				tc.change(&ports)
			}
			result, err := preflightCleanupRepositoryWithPorts(context.Background(), CleanupOptions{}, time.Time{}, nil, CleanupResult{}, "", ports)
			if tc.want == "" {
				if err != nil || result.Repository != "acme/app" {
					t.Fatalf("preflight result=%#v err=%v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("preflight result=%#v err=%v, want %q", result, err, tc.want)
			}
		})
	}
}
