//go:build darwin

package worktrees

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecureCanonicalCapabilityDeniesGitDirectoryMoveAfterFinalCheck(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	external := t.TempDir()
	ready := filepath.Join(fixture.canonical, "sandbox-ready")
	release := filepath.Join(fixture.canonical, "sandbox-release")
	script := filepath.Join(t.TempDir(), "wait-for-swap.sh")
	content := "#!/bin/sh\n" +
		": > " + shellQuote(ready) + "\n" +
		"while [ ! -f " + shellQuote(release) + " ]; do sleep 0.01; done\n" +
		"exec /usr/bin/git \"$@\"\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], SecureCanonicalGitHelperArgument, fixture.canonical, script, "config", "--local", "wb.capability", "must-not-write")
	command.ExtraFiles = []*os.File{canonical.root, canonical.common}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			t.Fatal("sandboxed Git did not reach the post-validation barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	heldGit := filepath.Join(external, "held-git")
	if err := os.Rename(filepath.Join(fixture.canonical, ".git"), heldGit); err != nil {
		_ = command.Process.Kill()
		t.Fatalf("move canonical Git directory after final validation: %v", err)
	}
	if err := os.Symlink(filepath.Join(external, "replacement-git"), filepath.Join(fixture.canonical, ".git")); err != nil {
		_ = command.Process.Kill()
		t.Fatalf("replace canonical Git directory after final validation: %v", err)
	}
	if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil {
		_ = command.Process.Kill()
		t.Fatal(err)
	}
	err = command.Wait()
	if err == nil || !strings.Contains(output.String(), "Operation not permitted") {
		t.Fatalf("post-validation canonical Git move result: err=%v output=%s", err, output.String())
	}
	value, readErr := gitConfigValue(heldGit, "wb.capability")
	if readErr == nil || value != "" {
		t.Fatalf("moved Git directory was mutated: value=%q err=%v", value, readErr)
	}
}

// TestSecureCanonicalCapabilityAllowsSSHUserIdentityResolution is the
// regression test for a real production outage: `wb worktree create` failed
// every canonical `git pull --ff-only` over SSH with OpenSSH's exact
// "No user exists for uid <n>" error, even though the account running WB
// unquestionably exists.
//
// The cause was the sandbox profile itself, not Git, not SSH configuration,
// and not the ambient environment (a bare `env -i ... git pull` succeeds).
// `sandboxProfile` denies everything by default and only re-opens file reads,
// networking, and a couple of write roots. OpenSSH's very first step in
// main() is `getpwuid(getuid())`, and on macOS that call is answered by
// opendirectoryd over Mach IPC — not by reading /etc/passwd, so the
// already-broad file-read* exception does not cover it. With no mach-lookup
// exception, that lookup fails inside the sandbox and SSH aborts before it
// can even locate ~/.ssh, let alone dial out.
//
// This test drives the exact seam production uses (a re-exec of this test
// binary through RunSecureCanonicalGitHelper, sandboxed by the real
// sandbox-exec profile from sandboxProfile) and substitutes /usr/bin/ssh for
// the "Git executable" so it exercises the real macOS directory-service
// mechanism without any network access: `ssh -G` only resolves
// configuration for the given destination and never dials out, so it stays
// hermetic while still calling getpwuid() exactly where OpenSSH always does.
//
// Every existing canonical-Git test in this suite uses a local file:// remote
// (see newGitFixture), so none of them ever exercised a getpwuid() call and
// none could have caught this: arg construction to gitCanonical was never
// wrong, only the sandbox surrounding the child process was too narrow.
func TestSecureCanonicalCapabilityAllowsSSHUserIdentityResolution(t *testing.T) {
	const sshExecutable = "/usr/bin/ssh"
	if info, err := os.Stat(sshExecutable); err != nil || info.Mode()&0o111 == 0 {
		t.Skipf("%s is unavailable in this environment: %v", sshExecutable, err)
	}
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	command := exec.Command(os.Args[0], SecureCanonicalGitHelperArgument, fixture.canonical, sshExecutable, "-G", "git@github.com")
	command.ExtraFiles = []*os.File{canonical.root, canonical.common}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("sandboxed ssh -G failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "No user exists for uid") {
		t.Fatalf("sandbox blocked the user-identity lookup SSH needs to authenticate: %s", output)
	}
}

func TestSecureStageCapabilityDeniesStageMoveAfterFinalCheck(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	operationRoot := t.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(operationRoot); resolveErr == nil {
		operationRoot = resolved
	}
	stagePath := filepath.Join(operationRoot, "stage")
	if err := os.Mkdir(stagePath, 0o700); err != nil {
		t.Fatal(err)
	}
	stage, err := openAbsoluteDirectoryNoFollow(stagePath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stage.Close() }()
	// The stage is deliberately not authorized to write the broader operation
	// root. Use the retained canonical resource for this test-only barrier.
	//
	// lockDarwinCapabilityParents strips the stage parent's write bit for the
	// whole sandboxed Git call, so the rename below never succeeds — the swap
	// this test simulates is denied earlier than Git's own sandbox profile
	// would have had a chance to see it.
	ready := filepath.Join(fixture.canonical, "sandbox-ready")
	release := filepath.Join(fixture.canonical, "sandbox-release")
	script := filepath.Join(t.TempDir(), "wait-for-stage-swap.sh")
	content := "#!/bin/sh\n" +
		": > " + shellQuote(ready) + "\n" +
		"while [ ! -f " + shellQuote(release) + " ]; do sleep 0.01; done\n" +
		"exec /usr/bin/git \"$@\"\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], SecureStageCanonicalGitHelperArgument, operationRoot, fixture.canonical, script, "feature/sandbox-stage", "main", "0")
	command.ExtraFiles = []*os.File{stage, canonical.root, canonical.common}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			t.Fatal("sandboxed Git did not reach the staged post-validation barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	external := t.TempDir()
	if err := os.Rename(stagePath, filepath.Join(external, "held-stage")); err == nil {
		_ = command.Process.Kill()
		t.Fatal("expected the frozen stage parent to deny moving the stage after final validation")
	} else if !os.IsPermission(err) {
		_ = command.Process.Kill()
		t.Fatalf("move held stage after final validation: unexpected error: %v", err)
	}
	if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil {
		_ = command.Process.Kill()
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("Git worktree add against the untouched stage failed: %v, output=%s", err, output.String())
	}
	if _, err := os.Stat(filepath.Join(stagePath, "checkout")); err != nil {
		t.Fatalf("expected the untouched stage to receive its checkout: %v", err)
	}
}

func TestSecureStageCapabilityDeniesBroaderOperationRootWrites(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	operationRoot := t.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(operationRoot); resolveErr == nil {
		operationRoot = resolved
	}
	stagePath := filepath.Join(operationRoot, "stage")
	if err := os.Mkdir(stagePath, 0o700); err != nil {
		t.Fatal(err)
	}
	stage, err := openAbsoluteDirectoryNoFollow(stagePath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stage.Close() }()
	broaderWrite := filepath.Join(operationRoot, "must-not-write")
	script := filepath.Join(t.TempDir(), "deny-operation-root-write.sh")
	content := "#!/bin/sh\n" +
		"if : > " + shellQuote(broaderWrite) + " 2>/dev/null; then\n" +
		"  echo 'unexpected operation-root write' >&2\n" +
		"  exit 99\n" +
		"fi\n" +
		"exec " + shellQuote(mustTrustedGitExecutable(t)) + " \"$@\"\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], SecureStageCanonicalGitHelperArgument, operationRoot, fixture.canonical, script, "feature/sandbox-root", "main", "0")
	command.ExtraFiles = []*os.File{stage, canonical.root, canonical.common}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("staged Git should succeed after refusing broader-root write: %v\n%s", err, output)
	}
	if _, err := os.Lstat(broaderWrite); !os.IsNotExist(err) {
		t.Fatalf("Git sandbox wrote outside its retained stage: %v", err)
	}
	checkout := filepath.Join(stagePath, "checkout")
	if _, err := os.Stat(checkout); err != nil {
		t.Fatalf("staged checkout was not created: %v", err)
	}
}

func TestDarwinCapabilityParentGuardRejectsRootSwapBeforeProfileStart(t *testing.T) {
	container := t.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(container); resolveErr == nil {
		container = resolved
	}
	rootPath := filepath.Join(container, "capability-root")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := openAbsoluteDirectoryNoFollow(rootPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	capability, err := newGitFilesystemCapability(gitFilesystemCapabilityRoot{path: rootPath, directory: root})
	if err != nil {
		t.Fatal(err)
	}
	guards, err := lockDarwinCapabilityParents(capability)
	if err != nil {
		t.Fatal(err)
	}
	defer restoreDarwinCapabilityParents(guards)
	external := t.TempDir()
	if err := os.Rename(rootPath, filepath.Join(external, "replacement")); err == nil {
		t.Fatal("capability root was swapped before sandbox profile start")
	}
	if !directoryStillMatches(rootPath, root) {
		t.Fatal("parent guard did not preserve the retained capability root")
	}
}

func mustTrustedGitExecutable(t *testing.T) string {
	t.Helper()
	executable, err := trustedGitExecutable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func gitConfigValue(gitDirectory, key string) (string, error) {
	command := exec.Command("/usr/bin/git", "--git-dir="+gitDirectory, "config", "--local", "--get", key)
	output, err := command.Output()
	return strings.TrimSpace(string(output)), err
}

// An HTTPS remote authenticates through `credential.helper`, and the default
// helper on macOS is osxkeychain, which reaches the keychain over Mach IPC to
// SecurityServer — not by reading a file, so `(allow file-read*)` does not
// cover it. Under `(deny default)` the helper returns nothing, Git falls back
// to prompting, and prompting is disabled for the canonical sandbox, so the
// remote fails with "could not read Username for 'https://...'" while the
// credential is sitting in the keychain.
//
// The sibling SSH test above fixed the same class of denial for SSH remotes
// and could not have caught this one: every fixture in this package uses a
// local file:// remote, so no test ever ran a credential helper at all. The
// fleet split cleanly along protocol lines — SSH repos worked, HTTPS repos
// did not — which is exactly the shape a missing Mach exception produces.
//
// This asserts the profile grants the lookup rather than driving a real
// authentication: it runs the credential helper directly under the same
// sandbox-exec profile with an input naming no host, so the helper does its
// keychain setup and exits without a network call and without needing a
// credential to exist.
func TestSecureCanonicalCapabilityAllowsKeychainCredentialLookup(t *testing.T) {
	if !strings.Contains(sandboxProfile(nil), "com.apple.SecurityServer") {
		t.Fatal("the canonical Git sandbox denies the keychain Mach lookup that credential.helper=osxkeychain needs; every HTTPS remote will fail with \"could not read Username\"")
	}

	const helper = "/usr/bin/git-credential-osxkeychain"
	if info, err := os.Stat(helper); err != nil || info.Mode()&0o111 == 0 {
		t.Skipf("%s is unavailable in this environment: %v", helper, err)
	}
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()

	command := exec.Command(os.Args[0], SecureCanonicalGitHelperArgument, fixture.canonical, helper, "get")
	command.ExtraFiles = []*os.File{canonical.root, canonical.common}
	command.Stdin = strings.NewReader("\n")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("sandboxed credential helper failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "Operation not permitted") {
		t.Fatalf("sandbox blocked the keychain lookup the credential helper needs: %s", output)
	}
}
