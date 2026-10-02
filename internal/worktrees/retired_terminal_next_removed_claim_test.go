package worktrees

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredTerminalNextRemovedClaimRequiresActualPrivateFiles(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"success", "claim_decode", "claim_identity", "terminal_decode", "terminal_identity"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			result := RetireResult{Task: "task", Repository: "acme/app", Branch: "topic", ClaimID: strings.Repeat("a", 64), EffortID: "effort", RunID: "run", Worktree: filepath.Join(home, "checkout"), SourceSHA: strings.Repeat("b", 40)}
			run := filepath.Join(home, "worklogs", result.EffortID, "runs", result.RunID)
			claim := workLogClaim{Task: result.Task, Repository: result.Repository, Branch: result.Branch, ClaimID: result.ClaimID, EffortID: result.EffortID, RunID: result.RunID, Worktree: result.Worktree}
			terminal := workLogTerminalRecord{ClaimID: result.ClaimID, FinalCommit: result.SourceSHA, Disposition: "retired"}
			write := func(kind string, value any) string {
				t.Helper()
				dir := filepath.Join(run, kind)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, result.ClaimID+".json")
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				return path
			}
			claimPath := write("claims", claim)
			terminalPath := write("terminals", terminal)
			if err := retireValidateRemovedClaim(home, result); err != nil {
				t.Fatalf("native control=%v", err)
			}
			switch boundary {
			case "claim_decode":
				if err := os.WriteFile(claimPath, []byte("{invalid-claim\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "claim_identity":
				claim.Task = "another"
				write("claims", claim)
			case "terminal_decode":
				if err := os.WriteFile(terminalPath, []byte("{invalid-terminal\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "terminal_identity":
				terminal.Disposition = "discarded"
				write("terminals", terminal)
			}
			claimBefore, err := os.ReadFile(claimPath)
			if err != nil {
				t.Fatal(err)
			}
			terminalBefore, err := os.ReadFile(terminalPath)
			if err != nil {
				t.Fatal(err)
			}
			err = retireValidateRemovedClaim(home, result)
			if boundary == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatalf("%s admitted changed private evidence", boundary)
			}
			if boundary == "claim_identity" && !strings.Contains(err.Error(), "conflicts with immutable Work Log claim") {
				t.Fatalf("claim refusal=%v", err)
			}
			if boundary == "terminal_identity" && !strings.Contains(err.Error(), "conflicts with immutable Work Log terminal") {
				t.Fatalf("terminal refusal=%v", err)
			}
			after, readErr := os.ReadFile(claimPath)
			if readErr != nil || string(after) != string(claimBefore) {
				t.Fatalf("claim rewritten:%q %v", after, readErr)
			}
			after, readErr = os.ReadFile(terminalPath)
			if readErr != nil || string(after) != string(terminalBefore) {
				t.Fatalf("terminal rewritten:%q %v", after, readErr)
			}
		})
	}
}
