package orchestrate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// engineGitSeeds belong to TestMain, not a test's TempDir: a completed test
// must not remove a seed while another fixture is cloning from it.
var engineGitSeeds struct {
	sync.Mutex
	root       string
	byBranch   map[string]string
	moduleSeed string
}

// engineGitSeed returns a committed source repository. Callers may clone it,
// but must put every mutable remote, checkout, ref, and WB record in their own
// test directory. Failed initialization is never cached.
func engineGitSeed(t *testing.T, branch string) string {
	t.Helper()
	seed, err := engineGitSeedForBranch(branch)
	if err != nil {
		t.Fatal(err)
	}
	return seed
}

func engineGitSeedForBranch(branch string) (string, error) {
	return engineGitSeedForBranchWithWrite(branch, os.WriteFile)
}

// The writer parameter lets the test prove that a failed recipe is removed
// and is not published to other fixture constructors.
func engineGitSeedForBranchWithWrite(branch string, writeFile func(string, []byte, os.FileMode) error) (string, error) {
	engineGitSeeds.Lock()
	defer engineGitSeeds.Unlock()
	if seed := engineGitSeeds.byBranch[branch]; seed != "" {
		return seed, nil
	}
	if engineGitSeeds.root == "" {
		return "", fmt.Errorf("TestMain did not establish the engine Git seed root")
	}
	if err := runEngineSeedGit(engineGitSeeds.root, "check-ref-format", "--branch", branch); err != nil {
		return "", err
	}
	seed, err := os.MkdirTemp(engineGitSeeds.root, "seed-")
	if err != nil {
		return "", err
	}
	err = writeFile(filepath.Join(seed, "dependency.txt"), []byte("old\n"), 0o644)
	if err == nil {
		err = commitEngineSeed(seed, branch)
	}
	if err != nil {
		_ = os.RemoveAll(seed)
		return "", err
	}
	if engineGitSeeds.byBranch == nil {
		engineGitSeeds.byBranch = make(map[string]string)
	}
	engineGitSeeds.byBranch[branch] = seed
	return seed, nil
}

func runEngineSeedGit(directory string, args ...string) error {
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s in engine seed: %w: %s", strings.Join(args, " "), err, output)
	}
	return nil
}

// pullRequestGitSeed shares only the immutable main/go.mod recipe. Its finite
// cache slot is separate from the engine's dependency.txt branch templates.
func pullRequestGitSeed(t *testing.T) string {
	t.Helper()
	engineGitSeeds.Lock()
	defer engineGitSeeds.Unlock()
	if engineGitSeeds.moduleSeed != "" {
		return engineGitSeeds.moduleSeed
	}
	seed, err := initializePullRequestGitSeed(engineGitSeeds.root, os.WriteFile)
	if err != nil {
		t.Fatal(err)
	}
	engineGitSeeds.moduleSeed = seed
	return seed
}

// Initialization owns its temporary directory until the complete recipe
// succeeds; a failed writer or Git command cannot publish a partial seed.
func initializePullRequestGitSeed(root string, writeFile func(string, []byte, os.FileMode) error) (string, error) {
	if root == "" {
		return "", fmt.Errorf("TestMain did not establish the engine Git seed root")
	}
	seed, err := os.MkdirTemp(root, "module-seed-")
	if err != nil {
		return "", err
	}
	err = writeFile(filepath.Join(seed, "go.mod"), []byte("module example.test/app\n\ngo 1.24\n"), 0o644)
	if err == nil {
		err = commitEngineSeed(seed, "main")
	}
	if err != nil {
		_ = os.RemoveAll(seed)
		return "", err
	}
	return seed, nil
}

// commitEngineSeed shares the native commit recipe while each caller owns its
// content, temporary directory, failed initialization and cache publication.
func commitEngineSeed(seed, branch string) error {
	var err error
	for _, args := range [][]string{
		{"init", "-b", branch},
		{"config", "user.name", "WB Test"},
		{"config", "user.email", "wb@example.test"},
		{"add", "-A"},
		{"commit", "-m", "initial"},
	} {
		if err = runEngineSeedGit(seed, args...); err != nil {
			break
		}
	}
	return err
}
