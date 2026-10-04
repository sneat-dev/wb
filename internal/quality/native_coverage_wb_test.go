package quality

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The root coordinator opts in to this actual WB journey; routine quality tests do not build WB.
func TestNativeCoverageActualWBVersionProfile(t *testing.T) {
	module := os.Getenv("WB_TEST_NATIVE_WB_COVERAGE")
	if module == "" {
		t.Skip("coordinator-owned actual WB coverage journey")
	}
	t.Parallel()
	directory := os.Getenv("WB_TEST_NATIVE_WB_REPORT")
	if directory == "" {
		directory = t.TempDir()
	} else if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(directory, "native-wb.cov")
	selector := `^TestHelpAndVersionAlwaysAnswerWithoutATerminal$/^wb_--version$`
	arguments := []string{"test", "-covermode=atomic", "-coverprofile=" + profile, "./cmd/wb", "-run=" + selector, "-v"}
	output, attempts, err := runGoCoverageCommand(context.Background(), RunOptions{Timeout: 2 * time.Minute}, module, profile, arguments)
	if err != nil || attempts != 1 || !strings.Contains(output, "--- PASS: TestHelpAndVersionAlwaysAnswerWithoutATerminal") {
		t.Fatalf("actual WB version %d %v\n%s", attempts, err, output)
	}
	_, blocks, err := readCoverageProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(module, "cmd/wb/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	line := 0
	for index, text := range strings.Split(string(source), "\n") {
		if strings.Contains(text, "os.Exit(dispatch(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))") {
			line = index + 1
			break
		}
	}
	if line == 0 {
		t.Fatal("actual native main exit statement missing")
	}
	found := false
	var mainBlock coverageBlock
	for _, block := range blocks {
		if !strings.HasPrefix(block.location, "github.com/sneat-dev/wb/cmd/wb/") {
			t.Fatalf("default coverage scope widened: %+v", block)
		}
		if !strings.Contains(block.location, "/cmd/wb/main.go:") {
			continue
		}
		_, positions, ok := strings.Cut(block.location, "main.go:")
		var start, startColumn, end, endColumn int
		scanned, scanErr := fmt.Sscanf(positions, "%d.%d,%d.%d", &start, &startColumn, &end, &endColumn)
		if ok && scanErr == nil && scanned == 4 && start <= line && end >= line {
			found = true
			mainBlock = block
			if block.count < 1 {
				t.Fatalf("actual native main was not counted: %+v", block)
			}
		}
	}
	if !found {
		t.Fatalf("native main line %d absent from final runner profile", line)
	}
	profileBytes, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	sourceHash := fmt.Sprintf("%x", sha256.Sum256(source))
	profileHash := fmt.Sprintf("%x", sha256.Sum256(profileBytes))
	receipt := map[string]any{"source_sha256": sourceHash, "profile_sha256": profileHash, "profile": profile, "main_location": mainBlock.location, "main_statements": mainBlock.statements, "main_count": mainBlock.count, "attempts": attempts, "selector": selector, "scope": "default cmd/wb only"}
	bytes, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "native-wb-receipt.json"), bytes, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual WB native coverage: %s", bytes)
}
