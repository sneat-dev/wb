package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRelocationMoveOperationOrdersDurableStages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		failStage string
		want      []string
		wantMove  bool
		wantPath  string
	}{
		{"intent failure", "intent", []string{"intent"}, false, ""},
		{"preparation failure", "prepare", []string{"intent", "prepare"}, false, ""},
		{"move failure", "move", []string{"intent", "prepare", "move"}, true, ""},
		{"interrupted after move", "after", []string{"intent", "prepare", "move", "after"}, true, ""},
		{"receipt failure", "receipt", []string{"intent", "prepare", "move", "after", "receipt"}, true, ""},
		{"success", "", []string{"intent", "prepare", "move", "after", "receipt"}, true, "receipt.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stages []string
			failure := errors.New("injected " + tc.failStage)
			stage := func(name string) error {
				stages = append(stages, name)
				if name == tc.failStage {
					return failure
				}
				return nil
			}
			intent := &workLogRelocationIntent{}
			move, path, err := runRelocationMove(context.Background(), relocationMoveRequest{
				now:         func() time.Time { return time.Now() },
				prepareMove: func() error { return stage("prepare") },
				afterMove:   func() error { return stage("after") },
			}, relocationMovePorts{
				appendIntent: func(string, workLogClaim, string, string, string, string, relocationPlacementRecord, time.Time) (*workLogRelocationIntent, string, error) {
					return intent, "", stage("intent")
				},
				move: func(context.Context, string, string, string, string, worktreeMoveHooks) (worktreeMoveOutcome, error) {
					return worktreeMoveOutcome{Moved: true, Repaired: true}, stage("move")
				},
				appendReceipt: func(_ string, _ workLogClaim, got *workLogRelocationIntent, _ time.Time) (*workLogRelocationReceipt, string, error) {
					if got != intent {
						t.Fatal("receipt received a different intent")
					}
					return &workLogRelocationReceipt{}, "receipt.json", stage("receipt")
				},
			})
			if !reflect.DeepEqual(stages, tc.want) || move.Moved != tc.wantMove || path != tc.wantPath {
				t.Fatalf("stages=%v, move=%+v, path=%q", stages, move, path)
			}
			if tc.failStage == "" && err != nil || tc.failStage != "" && !errors.Is(err, failure) {
				t.Fatalf("error = %v, want stage failure %q", err, tc.failStage)
			}
		})
	}
}

func TestRelocationMoveOperationWithoutOptionalStages(t *testing.T) {
	t.Parallel()
	_, path, err := runRelocationMove(context.Background(), relocationMoveRequest{now: time.Now}, relocationMovePorts{
		appendIntent: func(string, workLogClaim, string, string, string, string, relocationPlacementRecord, time.Time) (*workLogRelocationIntent, string, error) {
			return &workLogRelocationIntent{}, "", nil
		},
		move: func(context.Context, string, string, string, string, worktreeMoveHooks) (worktreeMoveOutcome, error) {
			return worktreeMoveOutcome{}, nil
		},
		appendReceipt: func(string, workLogClaim, *workLogRelocationIntent, time.Time) (*workLogRelocationReceipt, string, error) {
			return nil, "receipt.json", nil
		},
	})
	if err != nil || path != "receipt.json" {
		t.Fatalf("path=%q, err=%v", path, err)
	}
}

func TestRunRelocationMovePreservesPortFailuresAndReceiptProof(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, fail, want string
		missingRequired  bool
	}{
		{"intent error", "intent", "record intent: injected", true},
		{"move error", "move", "injected", true},
		{"receipt error", "receipt", "record receipt: injected", true},
		{"missing required receipt", "missing", "no receipt", true},
		{"reverse accepts nil receipt result", "missing", "", false},
		{"complete", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at := time.Date(2026, time.September, 30, 0, 0, 0, 0, time.UTC)
			request := relocationMoveRequest{home: "home", source: "verified-old", intentSource: "planned-old", destination: "new", to: "local", head: "sha",
				intentContext: "record intent", receiptContext: "record receipt", now: func() time.Time { return at }}
			if tc.missingRequired {
				request.missingReceipt = "no receipt"
			}
			intent := &workLogRelocationIntent{}
			ports := relocationMovePorts{
				appendIntent: func(home string, claim workLogClaim, source, dest, to, head string, placement relocationPlacementRecord, timestamp time.Time) (*workLogRelocationIntent, string, error) {
					if home != "home" || source != "planned-old" || dest != "new" || to != "local" || head != "sha" || timestamp != at {
						t.Fatalf("intent inputs drifted")
					}
					if tc.fail == "intent" {
						return nil, "", errors.New("injected")
					}
					return intent, "intent.json", nil
				},
				move: func(_ context.Context, _, _, source, dest string, _ worktreeMoveHooks) (worktreeMoveOutcome, error) {
					if source != "verified-old" || dest != "new" {
						t.Fatalf("physical move used %q -> %q", source, dest)
					}
					if tc.fail == "move" {
						return worktreeMoveOutcome{Moved: true}, errors.New("injected")
					}
					return worktreeMoveOutcome{Moved: true, Repaired: true}, nil
				},
				appendReceipt: func(_ string, _ workLogClaim, got *workLogRelocationIntent, timestamp time.Time) (*workLogRelocationReceipt, string, error) {
					if got != intent || timestamp != at {
						t.Fatal("receipt received wrong durable intent or time")
					}
					if tc.fail == "receipt" {
						return nil, "", errors.New("injected")
					}
					if tc.fail == "missing" {
						return nil, "", nil
					}
					return &workLogRelocationReceipt{}, "receipt.json", nil
				},
			}
			move, path, err := runRelocationMove(context.Background(), request, ports)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
			if tc.fail == "move" && !move.Moved {
				t.Fatal("partial move outcome was lost")
			}
			if tc.fail == "" && (!move.Repaired || path != "receipt.json") {
				t.Fatalf("move=%+v path=%q", move, path)
			}
		})
	}
}

func TestRelocationDestinationRootLegacySuffixFallback(t *testing.T) {
	t.Parallel()
	destination := filepath.Join(t.TempDir(), "task", "acme", "app")
	if got, want := relocationDestinationRoot(destination, "mismatched/repository", "shared"), filepath.Dir(filepath.Dir(filepath.Dir(destination))); got != want {
		t.Fatalf("root=%q, want %q", got, want)
	}
}

func TestRelocationPlacementKeepsOnlyDescendantPaths(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ root, destination, want string }{
		{"/tmp/root", "/tmp/root/task/app", "task/app"},
		{"/tmp/root", "/tmp/root", ""},
		{"/tmp/root", "/tmp/other", ""},
		{"/tmp/root", "relative", ""},
	} {
		if got := relocationPlacement(tc.root, tc.destination); got.Root != tc.root || got.Relative != tc.want {
			t.Errorf("relocationPlacement(%q, %q) = %+v, want relative %q", tc.root, tc.destination, got, tc.want)
		}
	}
}

func TestPrepareRelocationDestinationRejectsReplacedSharedRoot(t *testing.T) {
	t.Parallel()
	container := t.TempDir()
	root := filepath.Join(container, "shared")
	destination := filepath.Join(root, "task", "github.com", "acme", "app")
	err := prepareRelocationDestinationWithHooks(context.Background(), ListResult{}, "", destination, "github.com/acme/app", "shared", relocationDestinationHooks{
		afterSharedRootOpen: func() {
			if err := os.Rename(root, root+"-old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "shared relocation root changed") {
		t.Fatalf("replaced root error=%v", err)
	}
}

func TestPrepareRelocationDestinationRefusesUnsafePaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	relative := "github.com/acme/app"
	for _, tc := range []struct {
		name        string
		destination string
		setup       func(*testing.T, string)
		want        string
	}{
		{"missing repository suffix", filepath.Join(root, "task", "other"), nil, "no repository suffix"},
		{"invalid task", filepath.Join(root, "bad task", "github.com", "acme", "app"), nil, "invalid task segment"},
		{"symlinked task", filepath.Join(root, "symlink-task", "github.com", "acme", "app"), func(t *testing.T, path string) {
			if err := os.Symlink(t.TempDir(), filepath.Join(root, "symlink-task")); err != nil {
				t.Fatal(err)
			}
		}, "symlink"},
		{"symlinked parent", filepath.Join(root, "parent-task", "github.com", "acme", "app"), func(t *testing.T, path string) {
			if err := os.MkdirAll(filepath.Join(root, "parent-task"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), filepath.Join(root, "parent-task", "github.com")); err != nil {
				t.Fatal(err)
			}
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.setup != nil {
				tc.setup(t, tc.destination)
			}
			err := prepareRelocationDestination(context.Background(), ListResult{}, "", tc.destination, relative, "shared")
			if err == nil || tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("prepare error=%v, want refusal containing %q", err, tc.want)
			}
		})
	}
	fileRoot := filepath.Join(root, "file-root")
	if err := os.WriteFile(fileRoot, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := prepareRelocationDestination(context.Background(), ListResult{}, "", filepath.Join(fileRoot, "task", "github.com", "acme", "app"), relative, "shared")
	if err == nil || !strings.Contains(err.Error(), "open shared relocation root") {
		t.Fatalf("file root error=%v", err)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide HOME, XDG_CONFIG_HOME, and WB_PROJECTS_ROOT for real Git.
func TestPrepareLocalRelocationDestinationFailures(t *testing.T) {
	fixture := newGitFixture(t)
	entry := ListResult{CanonicalDir: fixture.canonical}
	local := filepath.Join(fixture.canonical, ".worktrees", "test-task")
	if err := prepareRelocationDestination(context.Background(), ListResult{CanonicalDir: filepath.Join(t.TempDir(), "missing")}, "HEAD", local, "", "local"); err == nil {
		t.Fatal("missing canonical clone was accepted")
	}
	if err := prepareRelocationDestination(context.Background(), entry, "not-a-revision", local, "", "local"); err == nil {
		t.Fatal("invalid base revision was accepted")
	}
	if _, _, err := prepareRelocationMove(context.Background(), fixture.projectsRoot, entry, "not-a-revision", local, "local"); err == nil {
		t.Fatal("move preparation accepted an invalid base revision")
	}
	if err := prepareRelocationDestination(context.Background(), entry, "HEAD", filepath.Join(t.TempDir(), "elsewhere"), "", "local"); err == nil || !strings.Contains(err.Error(), "local relocation destination root changed") {
		t.Fatalf("wrong local root error=%v", err)
	}
}

func TestPrepareRelocationMoveRejectsUnrelatedCanonicalPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, _, err := prepareRelocationMove(context.Background(), root, ListResult{CanonicalDir: filepath.Join(t.TempDir(), "other")}, "HEAD", filepath.Join(root, "task", "app"), "shared")
	if err == nil {
		t.Fatal("unrelated canonical path was accepted")
	}
}
