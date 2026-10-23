//go:build e2e

package quality

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2EDeadcodeDefaultAnalyzerHonorsCallerCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Deadcode(ctx, t.TempDir(), DeadcodeOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("default analyzer cancellation=%v", err)
	}
}

func TestE2EVerificationAttachesOnlyCompleteNativeDeadcodeFindings(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"findings", "other exit", "caller canceled"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "cmd", "wb"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/native-deadcode\n\ngo 1.27\n"), 0600); err != nil {
				t.Fatal(err)
			}
			code := 1
			if phase == "other exit" {
				code = 2
			}
			output := strings.TrimSuffix(deadcodeFailureOutput("example.test/pkg.Function"), "exit status 1\n")
			source := fmt.Sprintf("package main\nimport(\"fmt\";\"os\")\nfunc main(){fmt.Print(%q);os.Exit(%d)}\n", output, code)
			if err := os.WriteFile(filepath.Join(root, "cmd", "wb", "main.go"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "caller canceled" {
				cancel()
			}
			entry := runVerification(ctx, RunOptions{}, "go", ".", CheckLint, root, "go", "run", "./cmd/wb", "deadcode")
			if entry.Status != StatusFailed || entry.Deadcode == nil || entry.Deadcode.Valid() != (phase == "findings") {
				t.Fatalf("native evidence=%+v entry=%+v", entry.Deadcode, entry)
			}
			if phase == "findings" && (len(entry.Deadcode.Identities) != 1 || entry.Deadcode.Identities[0] != "example.test/pkg.Function") {
				t.Fatalf("findings=%+v", entry.Deadcode)
			}
		})
	}
}
