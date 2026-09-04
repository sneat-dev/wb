package worktrees

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalPolicyReaderIsReadOnlyAndDescriptorBound(t *testing.T) {
	fixture := newGitFixture(t)
	commitRepositoryBranchConfig(t, fixture, "version: 1\nworktrees:\n  branch_prefix: policy/\n", "policy")
	canonical, base := synchronizedBranchConfigBase(t, fixture)
	defer canonical.close()

	configPath := filepath.Join(fixture.canonical, ".git", "config")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	contents, found, err := repositoryBranchConfigAt(context.Background(), canonical, base)
	if err != nil {
		t.Fatal(err)
	}
	if !found || !strings.Contains(string(contents), "branch_prefix: policy/") {
		t.Fatalf("policy read = found %t, contents %q", found, contents)
	}
	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(configAfter) != string(configBefore) {
		t.Fatalf("read-only policy lookup changed canonical Git config\nbefore: %q\nafter:  %q", configBefore, configAfter)
	}
	for _, lock := range []string{filepath.Join(fixture.canonical, ".git", "index.lock"), filepath.Join(fixture.canonical, ".git", "config.lock")} {
		if _, err := os.Stat(lock); !os.IsNotExist(err) {
			t.Fatalf("read-only policy lookup left Git lock %s: %v", lock, err)
		}
	}
}

func TestSecureCanonicalPolicyHelperRejectsNonPolicyGitCommand(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	gitExecutable, err := trustedGitExecutable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], SecureCanonicalPolicyGitHelperArgument, fixture.canonical, gitExecutable, "status")
	command.ExtraFiles = []*os.File{canonical.root, canonical.common}
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "invalid read-only query") {
		t.Fatalf("non-policy helper command err=%v output=%s", err, output)
	}
}

func TestCanonicalPolicyReaderRejectsCanonicalRootSwapBeforeRead(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	base := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	moved := fixture.canonical + "-moved"
	external := t.TempDir()
	canonical.afterValidation = func() {
		if err := os.Rename(fixture.canonical, moved); err != nil {
			t.Fatalf("move canonical root: %v", err)
		}
		if err := os.Symlink(external, fixture.canonical); err != nil {
			t.Fatalf("replace canonical root: %v", err)
		}
	}
	_, err = gitCanonicalPolicyBytes(context.Background(), canonical, "show", base)
	if err == nil || !strings.Contains(err.Error(), "canonical repository path changed") {
		t.Fatalf("canonical root swap policy read error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(external, ".git")); !os.IsNotExist(err) {
		t.Fatalf("substituted canonical root was mutated: %v", err)
	}
}

func TestCanonicalPolicyReaderRejectsCanonicalGitDirectorySwapBeforeRead(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	base := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	parkedGit := filepath.Join(fixture.canonical, ".git-parked")
	canonical.afterValidation = func() {
		if err := os.Rename(filepath.Join(fixture.canonical, ".git"), parkedGit); err != nil {
			t.Fatalf("move canonical Git directory: %v", err)
		}
		if err := os.Mkdir(filepath.Join(fixture.canonical, ".git"), 0o755); err != nil {
			t.Fatalf("replace canonical Git directory: %v", err)
		}
	}
	_, err = gitCanonicalPolicyBytes(context.Background(), canonical, "show", base)
	if err == nil || !strings.Contains(err.Error(), "canonical Git directory changed") {
		t.Fatalf("canonical Git directory swap policy read error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.canonical, ".git", "HEAD")); !os.IsNotExist(err) {
		t.Fatalf("substituted canonical Git directory was mutated: %v", err)
	}
}

func TestSecureCanonicalPolicyHelperRejectsCanonicalRootSwapAfterRead(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	base := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	moved := fixture.canonical + "-moved"
	external := t.TempDir()
	script := filepath.Join(t.TempDir(), "swap-canonical.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+
		"mv \"$WB_TEST_POLICY_CANONICAL\" \"$WB_TEST_POLICY_MOVED\"\n"+
		"ln -s \"$WB_TEST_POLICY_EXTERNAL\" \"$WB_TEST_POLICY_CANONICAL\"\n"+
		"printf 'unexpected policy bytes'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], SecureCanonicalPolicyGitHelperArgument, fixture.canonical, script, "show", base)
	command.ExtraFiles = []*os.File{canonical.root, canonical.common}
	command.Env = append(os.Environ(),
		"WB_TEST_POLICY_CANONICAL="+fixture.canonical,
		"WB_TEST_POLICY_MOVED="+moved,
		"WB_TEST_POLICY_EXTERNAL="+external,
	)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "canonical repository changed during policy read") {
		t.Fatalf("post-read canonical root swap err=%v output=%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(external, ".git")); !os.IsNotExist(err) {
		t.Fatalf("substituted canonical root was mutated: %v", err)
	}
}

func TestSecureCanonicalPolicyHelperRejectsCanonicalGitDirectorySwapAfterRead(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	base := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	parkedGit := filepath.Join(fixture.canonical, ".git-parked")
	script := filepath.Join(t.TempDir(), "swap-git-directory.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+
		"mv \"$WB_TEST_POLICY_GIT\" \"$WB_TEST_POLICY_PARKED_GIT\"\n"+
		"mkdir \"$WB_TEST_POLICY_GIT\"\n"+
		"printf 'unexpected policy bytes'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], SecureCanonicalPolicyGitHelperArgument, fixture.canonical, script, "show", base)
	command.ExtraFiles = []*os.File{canonical.root, canonical.common}
	command.Env = append(os.Environ(),
		"WB_TEST_POLICY_GIT="+filepath.Join(fixture.canonical, ".git"),
		"WB_TEST_POLICY_PARKED_GIT="+parkedGit,
	)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "canonical repository changed during policy read") {
		t.Fatalf("post-read canonical Git directory swap err=%v output=%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(fixture.canonical, ".git", "HEAD")); !os.IsNotExist(err) {
		t.Fatalf("substituted canonical Git directory was mutated: %v", err)
	}
}
