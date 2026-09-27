package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerifyReceiptRejectsMalformedEvidenceAtItsNamedStage(t *testing.T) {
	t.Parallel()
	for _, stage := range []struct {
		flag  string
		index int
	}{
		{"--local-check", 0}, {"--ci-wait", 1}, {"--remote-target", 2},
		{"--deployed-revision", 3}, {"--terminal-cleanup", 4},
	} {
		t.Run(stage.flag, func(t *testing.T) {
			t.Parallel()
			paths, now := writeGraduationEvidence(t)
			files := []string{paths.localCheck, paths.ciWait, paths.remoteTarget, paths.deployed, paths.cleanup}
			if err := os.WriteFile(files[stage.index], []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--local-check", files[0], "--ci-wait", files[1], "--remote-target", files[2], "--deployed-revision", files[3], "--terminal-cleanup", files[4]}
			cmd := newVerifyReceiptCmdWithDeps(graduationCommandDeps{now: func() time.Time { return now }})
			cmd.SetArgs(args)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			var out bytes.Buffer
			cmd.SetOut(&out)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "decode "+stage.flag) {
				t.Fatalf("malformed %s error = %v", stage.flag, err)
			}
			if out.Len() != 0 {
				t.Fatalf("partial receipt leaked on malformed %s: %s", stage.flag, out.String())
			}
		})
	}
}

func TestVerifyRemoteTargetRejectsNoncanonicalGitObservation(t *testing.T) {
	t.Parallel()
	for _, observation := range []string{
		strings.Repeat("a", 40) + " refs/heads/main\n",
		strings.Repeat("a", 40) + "\trefs/heads/other\n",
		strings.Repeat("a", 40) + "\trefs/heads/main\n" + strings.Repeat("b", 40) + "\trefs/heads/main\n",
	} {
		t.Run(strings.ReplaceAll(strings.TrimSpace(observation), "\n", "_"), func(t *testing.T) {
			t.Parallel()
			deps := graduationCommandDeps{now: time.Now, runGit: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				switch args[0] {
				case "check-ref-format":
					return nil, nil
				case "remote":
					return []byte("git@github.com:sneat-dev/wb.git\n"), nil
				case "ls-remote":
					return []byte(observation), nil
				}
				return nil, errors.New("unexpected git command")
			}}
			cmd := newVerifyReceiptRemoteTargetCmd(deps)
			cmd.SetArgs([]string{"--repo", "sneat-dev/wb", "--target", "main", "--repository-path", t.TempDir()})
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "canonical exact target row") {
				t.Fatalf("observation %q error = %v", observation, err)
			}
		})
	}
}

func TestReadGraduationEvidenceRejectsDirectoryAndEmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := readGraduationEvidence(dir, "--local-check"); err == nil || !strings.Contains(err.Error(), "regular JSON evidence file") {
		t.Fatalf("directory error = %v", err)
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readGraduationEvidence(empty, "--ci-wait"); err == nil || !strings.Contains(err.Error(), "between 1 and") {
		t.Fatalf("empty file error = %v", err)
	}
}
