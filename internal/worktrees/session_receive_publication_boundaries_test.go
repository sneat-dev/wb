package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionReceiveHeldCanonicalRejectsClosedDescriptor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	held, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	canonical, err := openSessionReceiveCanonicalFromHeldRoot(root, held)
	if canonical != nil || err == nil || !strings.Contains(err.Error(), "retain canonical repository root") {
		t.Fatalf("closed held root opened: canonical=%v err=%v", canonical, err)
	}
}

func TestSessionReceiveCompletedStageRetirementBoundaries(t *testing.T) {
	t.Parallel()
	const stageName = ".wb-stage-0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name, want string
		prepare    func(*testing.T, string, string)
		wrongRoot  bool
		cancel     bool
		closeRoot  bool
		retired    bool
		change     func(*sessionReceivePublicationPorts)
	}{
		{name: "closed operation descriptor", closeRoot: true, want: "interrupted receive"},
		{name: "symlinked stage", prepare: func(t *testing.T, root, stage string) {
			if err := os.Symlink(t.TempDir(), stage); err != nil {
				t.Fatal(err)
			}
		}, want: "open completed interrupted receive stage"},
		{name: "mismatched operation root", prepare: mkdirInterruptedStage, wrongRoot: true, want: "path changed"},
		{name: "canceled verification", prepare: mkdirInterruptedStage, cancel: true, want: "verify completed interrupted receive stage"},
		{name: "nonempty stage", prepare: func(t *testing.T, root, stage string) {
			mkdirInterruptedStage(t, root, stage)
			if err := os.WriteFile(filepath.Join(stage, "retained"), []byte("evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: "not empty"},
		{name: "stage emptiness read fails", prepare: mkdirInterruptedStage, want: "inspect completed interrupted receive stage", change: func(p *sessionReceivePublicationPorts) {
			p.empty = func(*os.File) (bool, error) { return false, errors.New("stage read failed") }
		}},
		{name: "stage quarantine fails", prepare: mkdirInterruptedStage, want: "retire completed interrupted receive stage", change: func(p *sessionReceivePublicationPorts) {
			p.quarantine = func(*os.File, *os.File) error { return errors.New("stage quarantine failed") }
		}},
		{name: "empty stage retirement", prepare: mkdirInterruptedStage, retired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			stage := filepath.Join(root, stageName)
			if tc.prepare != nil {
				tc.prepare(t, root, stage)
			}
			directory, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = directory.Close() })
			if tc.closeRoot {
				_ = directory.Close()
			}
			operationRoot := root
			if tc.wrongRoot {
				operationRoot = t.TempDir()
			}
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if tc.change == nil {
				err = retireCompletedInterruptedSessionStage(ctx, operationRoot, directory)
			} else {
				ports := productionSessionReceivePublicationPorts()
				tc.change(&ports)
				err = retireCompletedInterruptedSessionStageWithPorts(ctx, operationRoot, directory, ports)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("retire error = %v, want %q", err, tc.want)
			}
			if tc.want == "" && err != nil {
				t.Fatal(err)
			}
			if tc.retired {
				if _, statErr := os.Lstat(stage); !os.IsNotExist(statErr) {
					t.Fatalf("stage remains: %v", statErr)
				}
			}
			if tc.want == "not empty" {
				if _, statErr := os.Lstat(filepath.Join(stage, "retained")); statErr != nil {
					t.Fatalf("retained evidence lost: %v", statErr)
				}
			}
		})
	}
}

func mkdirInterruptedStage(t *testing.T, _, stage string) {
	t.Helper()
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
}
