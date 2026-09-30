package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestRelocateRejectsInvalidTaskDirectionAndMissingTask(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, tc := range []struct {
		options RelocateOptions
		want    string
	}{
		{RelocateOptions{ProjectsRoot: root, To: "local"}, "task is required"},
		{RelocateOptions{ProjectsRoot: root, Task: "task", To: "elsewhere"}, "--to must be local or shared"},
		{RelocateOptions{ProjectsRoot: root, Task: "task", To: "local"}, "was not found"},
	} {
		_, err := Relocate(context.Background(), tc.options)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Relocate(%+v) error=%v, want %q", tc.options, err, tc.want)
		}
	}
}

func TestPlanRelocationRefusesMissingClaim(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolution, err := wbhome.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := planRelocation(context.Background(), resolution, RelocateOptions{ProjectsRoot: root, To: "local"}, ListResult{
		Task: "missing-claim", Repository: "acme/app", WorktreeDir: filepath.Join(root, "unclaimed"),
	})
	if err != nil || result.Eligible || !strings.Contains(result.Reason, "not corroborated") {
		t.Fatalf("missing-claim plan=%+v, err=%v", result, err)
	}
}

func TestReverseRelocationRefusesInvalidPathAndMissingClaim(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := ReverseRelocation(context.Background(), root, root, "relative", filepath.Join(root, "source"), time.Now()); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("relative reversal error=%v", err)
	}
	source := filepath.Join(root, "occupied")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ReverseRelocation(context.Background(), root, root, filepath.Join(root, "destination"), source, time.Now()); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("occupied reversal error=%v", err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := ReverseRelocation(context.Background(), root, root, filepath.Join(root, "unclaimed"), source, time.Now()); err == nil || !strings.Contains(err.Error(), "recheck Work Log claim") {
		t.Fatalf("unclaimed reversal error=%v", err)
	}
}

func TestRelocateCheckoutRefusesUnclaimedSource(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	result, err := RelocateCheckout(context.Background(), RelocateCheckoutOptions{
		ProjectsRoot: root, CanonicalDir: filepath.Join(root, "acme", "app"), Source: filepath.Join(root, "unclaimed"), Destination: filepath.Join(root, "dest"), To: "shared",
	})
	if err != nil || result.Eligible || !strings.Contains(result.Reason, "not corroborated") {
		t.Fatalf("unclaimed checkout result=%+v, err=%v", result, err)
	}
}

func TestRelocateEntryPortsPreserveFailureBoundaries(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"resolve", "list", "plan", "operation", "lock", "ineligible", "lost", "already", "finalize", "apply", "success"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			home := filepath.Join(root, "home")
			entry := ListResult{Task: "relocate-entry", WorktreeDir: filepath.Join(root, "source")}
			planned := RelocateResult{WorktreeDir: entry.WorktreeDir, Eligible: true}
			failure := errors.New("injected " + stage)
			if stage == "operation" {
				home = filepath.Join(root, "occupied-home")
				if err := os.WriteFile(home, []byte("occupied"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ports := relocationEntryPorts{
				resolve: func(string) (wbhome.Resolution, error) {
					if stage == "resolve" {
						return wbhome.Resolution{}, failure
					}
					return wbhome.Resolution{Write: wbhome.Layout{Home: home}}, nil
				},
				list: func(context.Context, ListOptions) (ListOutcome, error) {
					if stage == "list" {
						return ListOutcome{}, failure
					}
					return ListOutcome{Results: []ListResult{entry}}, nil
				},
				plan: func(context.Context, wbhome.Resolution, RelocateOptions, ListResult) (RelocateResult, error) {
					if stage == "plan" {
						return RelocateResult{}, failure
					}
					result := planned
					switch stage {
					case "ineligible":
						result.Eligible = false
					case "lost":
						result.WorktreeDir = filepath.Join(root, "other")
					case "already", "finalize":
						result.AlreadyThere = true
					}
					if stage == "finalize" {
						result.RecoveryPending = true
					}
					return result, nil
				},
				finalize: func(string, RelocateOptions, ListResult, *RelocateResult) error {
					if stage != "finalize" {
						t.Fatal("unexpected finalize")
					}
					return failure
				},
				apply: func(context.Context, string, RelocateOptions, ListResult, *RelocateResult) error {
					if stage != "apply" && stage != "success" {
						t.Fatal("unexpected apply")
					}
					if stage == "apply" {
						return failure
					}
					return nil
				},
			}
			if stage == "lock" {
				operation, err := prepareOperationRoot(home, entry.Task, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer operation.close()
				lock, err := acquireLockAt(operation.Directory, entry.Task)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.release() }()
			}
			outcome, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: root, Task: entry.Task, To: "local", Apply: true, ports: &ports})
			wantError := stage == "resolve" || stage == "list" || stage == "plan" || stage == "operation" || stage == "lock" || stage == "lost" || stage == "finalize" || stage == "apply"
			if wantError && err == nil || !wantError && err != nil {
				t.Fatalf("stage=%s outcome=%+v err=%v", stage, outcome, err)
			}
			if stage == "finalize" || stage == "apply" || stage == "plan" {
				if !errors.Is(err, failure) {
					t.Fatalf("stage=%s lost injected error: %v", stage, err)
				}
			}
		})
	}
}

func TestPlanRelocationPortsRefuseUnverifiedEvidence(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"placement", "path", "receipt", "pending", "stat"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			placement := WorktreePlacement{Root: root, relative: "acme/app"}
			destination, err := placement.Path("task", "acme/app")
			if err != nil {
				t.Fatal(err)
			}
			entry := ListResult{Task: "task", Repository: "acme/app", CanonicalDir: filepath.Join(root, "canonical"), WorktreeDir: filepath.Join(root, "source"), Clean: true}
			if stage == "path" {
				entry.Repository = "invalid repository"
			}
			if stage == "receipt" || stage == "pending" {
				entry.WorktreeDir = destination
			}
			failure := errors.New("injected " + stage)
			ports := relocationPlanPorts{
				claim: func(wbhome.Resolution, string) (workLogClaim, workLogProjection, string, string, error) {
					return workLogClaim{ClaimID: "claim"}, workLogProjection{}, "", root, nil
				},
				placement: func(string, string) (WorktreePlacement, error) {
					if stage == "placement" {
						return WorktreePlacement{}, failure
					}
					return placement, nil
				},
				receipt: func(string, workLogClaim, string) (*workLogRelocationReceipt, string, error) {
					if stage == "receipt" {
						return nil, "", failure
					}
					return nil, "", nil
				},
				pending: func(string, workLogClaim, string, string, string) (*workLogRelocationIntent, string, error) {
					if stage == "pending" {
						return nil, "", failure
					}
					return nil, "", nil
				},
				lstat: func(string) (os.FileInfo, error) { return nil, failure },
			}
			result, err := planRelocation(context.Background(), wbhome.Resolution{}, RelocateOptions{ProjectsRoot: root, To: "shared", planPorts: &ports}, entry)
			if stage == "path" {
				if err == nil {
					t.Fatalf("invalid repository path accepted: %+v", result)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("stage=%s plan=%+v err=%v", stage, result, err)
			}
		})
	}
}

func TestApplyRelocationPortsRecheckEveryAuthority(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"recheck", "safety", "claim", "identity", "occupied", "stat", "prepare", "move", "success"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			failure := errors.New("injected " + stage)
			planned := ListResult{Repository: "acme/app", WorktreeDir: "/planned", HeadSHA: "head", Branch: "topic"}
			refreshed := planned
			refreshed.WorktreeDir = "/verified"
			refreshed.Clean = true
			if stage == "safety" {
				refreshed.Clean = false
			}
			result := RelocateResult{WorktreeDir: planned.WorktreeDir, Destination: "/destination", HeadSHA: "head", Branch: "topic", ClaimID: "claim"}
			ports := relocationApplyPorts{
				recheck: func(context.Context, RelocateOptions, ListResult) (ListResult, error) {
					if stage == "recheck" {
						return ListResult{}, failure
					}
					return refreshed, nil
				},
				claim: func(_, path string) (workLogClaim, workLogProjection, string, error) {
					if path != "/verified" {
						t.Fatalf("claim checked %q, want verified path", path)
					}
					if stage == "claim" {
						return workLogClaim{}, workLogProjection{}, "", failure
					}
					id := "claim"
					if stage == "identity" {
						id = "other"
					}
					return workLogClaim{ClaimID: id}, workLogProjection{}, "", nil
				},
				lstat: func(string) (os.FileInfo, error) {
					switch stage {
					case "occupied":
						return nil, nil
					case "stat":
						return nil, failure
					default:
						return nil, os.ErrNotExist
					}
				},
				prepare: func(context.Context, string, ListResult, string, string, string) (string, relocationPlacementRecord, error) {
					if stage == "prepare" {
						return "", relocationPlacementRecord{}, failure
					}
					return "/root", relocationPlacementRecord{Root: "/root"}, nil
				},
				move: func(_ context.Context, request relocationMoveRequest) (worktreeMoveOutcome, string, error) {
					if request.source != "/verified" || request.intentSource != "/planned" {
						t.Fatalf("move authority drift: source=%q intent=%q", request.source, request.intentSource)
					}
					if stage == "move" {
						return worktreeMoveOutcome{Repaired: true}, "", failure
					}
					return worktreeMoveOutcome{Repaired: true}, "receipt.json", nil
				},
			}
			err := applyRelocation(context.Background(), "home", RelocateOptions{To: "local", applyPorts: &ports}, planned, &result)
			if stage == "success" {
				if err != nil || !result.Applied || !result.Finalized || !result.Repaired || result.ReceiptPath != "receipt.json" {
					t.Fatalf("success result=%+v err=%v", result, err)
				}
			} else if err == nil {
				t.Fatalf("stage=%s unexpectedly succeeded: %+v", stage, result)
			}
			if stage == "move" && !result.Repaired {
				t.Fatal("repair outcome was lost on move failure")
			}
		})
	}
}

func TestFinalizeInterruptedRelocationPortsRequireSameClaimAndIntent(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"safety", "claim", "identity", "pending", "missing", "receipt", "success"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			failure := errors.New("injected " + stage)
			entry := ListResult{Repository: "acme/app", WorktreeDir: "/moved", Branch: "topic", HeadSHA: "head", Clean: true}
			if stage == "safety" {
				entry.Clean = false
			}
			result := RelocateResult{ClaimID: "claim"}
			ports := relocationFinalizePorts{
				claim: func(_, path string) (workLogClaim, workLogProjection, string, error) {
					if path != "/moved" {
						t.Fatalf("claim path=%q", path)
					}
					if stage == "claim" {
						return workLogClaim{}, workLogProjection{}, "", failure
					}
					id := "claim"
					if stage == "identity" {
						id = "other"
					}
					return workLogClaim{ClaimID: id}, workLogProjection{}, "", nil
				},
				pending: func(_ string, _ workLogClaim, path, branch, head string) (*workLogRelocationIntent, string, error) {
					if path != "/moved" || branch != "topic" || head != "head" {
						t.Fatal("pending intent lookup drifted")
					}
					if stage == "pending" {
						return nil, "", failure
					}
					if stage == "missing" {
						return nil, "", nil
					}
					return &workLogRelocationIntent{}, "intent.json", nil
				},
				receipt: func(_ string, _ workLogClaim, intent *workLogRelocationIntent, _ time.Time) (*workLogRelocationReceipt, string, error) {
					if intent == nil {
						t.Fatal("nil intent published")
					}
					if stage == "receipt" {
						return nil, "", failure
					}
					return &workLogRelocationReceipt{}, "receipt.json", nil
				},
			}
			err := finalizeInterruptedRelocation("home", RelocateOptions{Now: time.Now, finalizePorts: &ports}, entry, &result)
			if stage == "success" {
				if err != nil || !result.Applied || !result.Finalized || result.ReceiptPath != "receipt.json" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil {
				t.Fatalf("stage=%s unexpectedly finalized", stage)
			}
		})
	}
}

func TestReverseRelocationPortsPreserveFinishedClaimAuthority(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"relative", "occupied", "stat", "resolve", "claim", "head", "prepare", "move", "terminal"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			destination, source := filepath.Join(root, "moved"), filepath.Join(root, "restore")
			if stage == "relative" {
				source = "relative"
			}
			failure := errors.New("injected " + stage)
			ports := relocationReversePorts{
				lstat: func(string) (os.FileInfo, error) {
					switch stage {
					case "occupied":
						return nil, nil
					case "stat":
						return nil, failure
					default:
						return nil, os.ErrNotExist
					}
				},
				resolve: func(string) (wbhome.Resolution, error) {
					if stage == "resolve" {
						return wbhome.Resolution{}, failure
					}
					return wbhome.Resolution{}, nil
				},
				claim: func(wbhome.Resolution, string) (workLogClaim, *workLogTerminalRecord, string, error) {
					if stage == "claim" {
						return workLogClaim{}, nil, "", failure
					}
					if stage == "terminal" {
						return workLogClaim{ClaimID: "claim"}, &workLogTerminalRecord{}, "home", nil
					}
					return workLogClaim{ClaimID: "claim"}, nil, "home", nil
				},
				head: func(context.Context, string) (string, error) {
					if stage == "head" {
						return "", failure
					}
					return " head\n", nil
				},
				prepareParent: func(string) error {
					if stage == "prepare" {
						return failure
					}
					return nil
				},
				move: func(_ context.Context, request relocationMoveRequest) (worktreeMoveOutcome, string, error) {
					if request.head != "head" || request.source != destination || request.intentSource != destination || request.destination != source || request.destinationRoot != filepath.Dir(source) || request.missingReceipt != "" {
						t.Fatalf("reversal request drifted: %+v", request)
					}
					if err := request.prepareMove(); err != nil {
						return worktreeMoveOutcome{}, "", err
					}
					if stage == "move" {
						return worktreeMoveOutcome{}, "", failure
					}
					return worktreeMoveOutcome{Moved: true, Repaired: true}, "receipt.json", nil
				},
			}
			err := reverseRelocationWithPorts(context.Background(), root, filepath.Join(root, "canonical"), destination, source, time.Now(), ports)
			if stage == "terminal" {
				if err != nil {
					t.Fatalf("finished claim refused: %v", err)
				}
			} else if err == nil {
				t.Fatalf("stage=%s unexpectedly succeeded", stage)
			}
		})
	}
}

func TestRelocateCheckoutPortsRecheckBeforeDurableMove(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"resolve", "claim", "safety-plan", "reason-plan", "occupied", "stat", "operation", "lock", "dry", "safety-apply", "reason-apply", "head", "prepare", "move", "success"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			options := RelocateCheckoutOptions{ProjectsRoot: root, CanonicalDir: filepath.Join(root, "canonical"), Source: filepath.Join(root, "source"), Destination: filepath.Join(root, "destination"), To: "shared", Apply: stage != "dry"}
			failure := errors.New("injected " + stage)
			ports := productionRelocationCheckoutPorts()
			ports.resolve = func(string) (wbhome.Resolution, error) {
				if stage == "resolve" {
					return wbhome.Resolution{}, failure
				}
				return wbhome.Resolution{}, nil
			}
			ports.claim = func(wbhome.Resolution, string) (workLogClaim, *workLogTerminalRecord, string, error) {
				if stage == "claim" {
					return workLogClaim{}, nil, "", failure
				}
				return workLogClaim{Task: "task", ClaimID: "claim", BaseSHA: "base"}, nil, filepath.Join(root, "home"), nil
			}
			calls := 0
			ports.safety = func(context.Context, string, string) (string, error) {
				calls++
				if stage == "safety-plan" && calls == 1 || stage == "safety-apply" && calls == 2 {
					return "", failure
				}
				if stage == "reason-plan" && calls == 1 || stage == "reason-apply" && calls == 2 {
					return "busy checkout", nil
				}
				return "", nil
			}
			ports.lstat = func(string) (os.FileInfo, error) {
				switch stage {
				case "occupied":
					return nil, nil
				case "stat":
					return nil, failure
				default:
					return nil, os.ErrNotExist
				}
			}
			if stage == "operation" {
				ports.operationRoot = func(string, string) (preparedOperationRoot, error) { return preparedOperationRoot{}, failure }
			}
			if stage == "lock" {
				ports.lock = func(*os.File, string) (operationLock, error) { return operationLock{}, failure }
			}
			ports.head = func(context.Context, string) (string, error) {
				if stage == "head" {
					return "", failure
				}
				return " head\n", nil
			}
			ports.prepare = func(context.Context, string, ListResult, string, string, string) (string, relocationPlacementRecord, error) {
				if stage == "prepare" {
					return "", relocationPlacementRecord{}, failure
				}
				return filepath.Join(root, "shared"), relocationPlacementRecord{Root: filepath.Join(root, "shared")}, nil
			}
			ports.move = func(_ context.Context, request relocationMoveRequest) (worktreeMoveOutcome, string, error) {
				if request.source != options.Source || request.intentSource != options.Source || request.head != "head" || request.home != filepath.Join(root, "home") {
					t.Fatalf("checkout move request drifted: %+v", request)
				}
				if stage == "move" {
					return worktreeMoveOutcome{Repaired: true}, "", failure
				}
				return worktreeMoveOutcome{Repaired: true}, "receipt.json", nil
			}
			options.ports = &ports
			result, err := RelocateCheckout(context.Background(), options)
			if stage == "success" {
				if err != nil || !result.Applied || !result.Repaired || result.ReceiptPath != "receipt.json" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				return
			}
			if stage == "dry" {
				if err != nil || !result.Eligible || result.Applied {
					t.Fatalf("dry result=%+v err=%v", result, err)
				}
				return
			}
			if stage == "claim" || stage == "reason-plan" || stage == "occupied" || stage == "lock" {
				if err != nil || result.Reason == "" {
					t.Fatalf("refusal result=%+v err=%v", result, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("stage=%s unexpectedly succeeded: %+v", stage, result)
			}
			if stage == "move" && !result.Repaired {
				t.Fatal("partial repair outcome was lost")
			}
		})
	}
}
