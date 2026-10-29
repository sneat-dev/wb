//go:build linux

package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testContainerWorkspaceDirs prepares three disjoint real directories outside
// the owner home. EvalSymlinks keeps the test honest on a controller whose
// temporary root is itself a symlink.
func testContainerWorkspaceDirs(t *testing.T) (worktree, gitCommon, input string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve test temp root: %v", err)
	}
	worktree = filepath.Join(root, "worktree")
	gitCommon = filepath.Join(root, "git-common")
	input = filepath.Join(root, "input")
	for _, directory := range []string{worktree, gitCommon, input} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("create workspace test directory %s: %v", directory, err)
		}
	}
	return worktree, gitCommon, input
}

func testContainerWorkspaceHost() *containerHost {
	return &containerHost{
		dockerPath:  func() (string, error) { return "/test/docker", nil },
		currentUser: func() (int, int) { return 501, 20 },
		userHome:    func() (string, error) { return "/home/testowner", nil },
		lookupEnv:   func(string) (string, bool) { return "", false },
	}
}

func testContainerWorkspaceOptions(stateDir string, fake *fakeContainerDocker, workspace *ContainerWorkspace) ContainerRuntimeOptions {
	options := testContainerRuntimeOptions(stateDir, fake)
	options.host = testContainerWorkspaceHost()
	options.Workspace = workspace
	return options
}

func testContainerWorkspacePlan(t *testing.T, options ContainerRuntimeOptions, workspace *ContainerWorkspace) containerWorkspacePlan {
	t.Helper()
	plan, err := normalizeContainerWorkspace(workspace, options.host)
	if err != nil {
		t.Fatalf("normalizeContainerWorkspace: %v", err)
	}
	return plan
}

// testContainerWorkspaceDocument builds the real inspect shape: one array with
// one object. A bind appears as a typed Mounts element and in HostConfig.Binds.
// A mask created with --tmpfs <target>:<options> appears only in
// HostConfig.Tmpfs, never in the top-level Mounts array, exactly as the daemon
// reports it.
func testContainerWorkspaceDocument(t *testing.T, containerID, name, runID, nonce, owner string, policy ExecutionPolicy, mounts []containerMount) []byte {
	t.Helper()
	raw := testContainerRuntimeDocument(t, containerID, name, runID, nonce, owner, policy, true)
	var documents []containerInspectDocument
	if err := json.Unmarshal(raw, &documents); err != nil {
		t.Fatalf("decode base inspect fixture: %v", err)
	}
	document := documents[0]
	document.HostConfig.Binds = []string{}
	document.HostConfig.Tmpfs = map[string]string{}
	for _, mount := range mounts {
		if mount.kind == containerMountTmpfs {
			document.HostConfig.Tmpfs[mount.target] = containerTmpfsOptions
			continue
		}
		document.Mounts = append(document.Mounts, containerInspectMount{
			Type:        string(mount.kind),
			Source:      mount.source,
			Destination: mount.target,
			RW:          mount.writable,
		})
		mode := "rw"
		if !mount.writable {
			mode = "ro"
		}
		document.HostConfig.Binds = append(document.HostConfig.Binds, mount.source+":"+mount.target+":"+mode)
	}
	raw, err := json.Marshal([]containerInspectDocument{document})
	if err != nil {
		t.Fatalf("marshal workspace inspect fixture: %v", err)
	}
	return raw
}

// testContainerWorkspaceInspect decodes the workspace fixture into the exact
// document shape verifyContainerHostConfig consumes.
func testContainerWorkspaceInspect(t *testing.T, policy ExecutionPolicy, plan containerWorkspacePlan) containerInspectDocument {
	t.Helper()
	raw := testContainerWorkspaceDocument(t, testContainerID, "wb-agent", testContainerRunID, "nonce", testContainerRuntimeOwner, policy, plan.mounts)
	var documents []containerInspectDocument
	if err := json.Unmarshal(raw, &documents); err != nil {
		t.Fatalf("decode workspace inspect fixture: %v", err)
	}
	return documents[0]
}

func newContainerWorkspaceReadyFake(t *testing.T, policy ExecutionPolicy, plan containerWorkspacePlan) *fakeContainerDocker {
	t.Helper()
	fake := newReadyFake(t, policy)
	fake.onCreate = func(args []string, containerID string) {
		name, runID, nonce, owner := testContainerCreateIdentity(t, args)
		fake.setDocument(containerID, testContainerWorkspaceDocument(t, containerID, name, runID, nonce, owner, policy, plan.mounts))
	}
	return fake
}

func TestContainerWorkspacePlansExactTrustedMountsAndMasks(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, input := testContainerWorkspaceDirs(t)

	// Host content that must not be mounted: Mac dependency trees, package
	// contents below a masked node_modules, and git objects below .git.
	for _, directory := range []string{
		filepath.Join(worktree, "node_modules"),
		filepath.Join(worktree, "node_modules", "host-only-dependency"),
		filepath.Join(worktree, "node_modules", "nested", "node_modules"),
		filepath.Join(worktree, "pkg", "node_modules"),
		filepath.Join(worktree, "pkg", "node_modules", "host-only-dependency"),
		filepath.Join(worktree, "pkg", ".nx"),
		filepath.Join(worktree, "pkg", ".pnpm-store"),
		filepath.Join(worktree, "pkg", "src"),
		filepath.Join(worktree, ".git", "objects"),
		filepath.Join(worktree, ".nx"),
		filepath.Join(worktree, ".pnpm-store"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("create workspace fixture %s: %v", directory, err)
		}
	}

	workspace := &ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon, InputDir: input}
	options := testContainerWorkspaceOptions(testContainerSlotStateDir(t), newFakeContainerDocker(), workspace)
	plan := testContainerWorkspacePlan(t, options, workspace)
	fake := newContainerWorkspaceReadyFake(t, executionTestPolicy(), plan)
	options.commands = fake.commands()

	runtime, err := OpenContainerRuntime(context.Background(), options)
	if err != nil {
		t.Fatalf("OpenContainerRuntime with a trusted workspace: %v", err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	createArgs := fake.createCall(t)
	var gotBinds, gotTmpfs []string
	for index, argument := range createArgs {
		switch argument {
		case "--mount":
			if index+1 >= len(createArgs) {
				t.Fatalf("docker create has a dangling --mount: %v", createArgs)
			}
			gotBinds = append(gotBinds, createArgs[index+1])
		case "--tmpfs":
			if index+1 >= len(createArgs) {
				t.Fatalf("docker create has a dangling --tmpfs: %v", createArgs)
			}
			gotTmpfs = append(gotTmpfs, createArgs[index+1])
		}
	}
	wantBinds := []string{
		"type=bind,source=" + worktree + ",target=" + worktree,
		"type=bind,source=" + gitCommon + ",target=" + gitCommon + ",readonly",
		"type=bind,source=" + input + ",target=" + containerInputTarget + ",readonly",
	}
	if !reflect.DeepEqual(gotBinds, wantBinds) {
		t.Fatalf("docker create bind mount arguments = %#v, want %#v", gotBinds, wantBinds)
	}
	wantTmpfs := []string{
		filepath.Join(worktree, "node_modules") + ":" + containerTmpfsOptions,
		filepath.Join(worktree, ".nx") + ":" + containerTmpfsOptions,
		filepath.Join(worktree, ".pnpm-store") + ":" + containerTmpfsOptions,
		filepath.Join(worktree, "pkg", ".nx") + ":" + containerTmpfsOptions,
		filepath.Join(worktree, "pkg", ".pnpm-store") + ":" + containerTmpfsOptions,
		filepath.Join(worktree, "pkg", "node_modules") + ":" + containerTmpfsOptions,
	}
	if !reflect.DeepEqual(gotTmpfs, wantTmpfs) {
		t.Fatalf("docker create tmpfs arguments = %#v, want %#v", gotTmpfs, wantTmpfs)
	}
	for _, argument := range append(append([]string{}, gotBinds...), gotTmpfs...) {
		if strings.Contains(argument, "host-only-dependency") || strings.Contains(argument, ".git") {
			t.Fatalf("a dependency or git object below a masked directory was mounted: %q", argument)
		}
	}
	for _, argument := range gotTmpfs {
		if !strings.HasSuffix(argument, ":"+containerTmpfsOptions) {
			t.Fatalf("tmpfs mask %q does not carry the fixed executable options %q", argument, containerTmpfsOptions)
		}
	}
	for _, argument := range createArgs {
		if argument == "--volume" || argument == "-v" || argument == "--publish" || argument == "-p" ||
			argument == "--network" || argument == "--pid" || argument == "--privileged" || argument == "--device" {
			t.Fatalf("docker create contains a forbidden option %q: %v", argument, createArgs)
		}
		if strings.Contains(argument, "docker.sock") {
			t.Fatalf("docker create mounts a Docker socket: %v", createArgs)
		}
	}
}

func TestContainerWorkspaceMasksRootNodeModulesEvenWhenAbsentAndOmitsOptionalInput(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, _ := testContainerWorkspaceDirs(t)
	plan, err := normalizeContainerWorkspace(&ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon}, testContainerWorkspaceHost())
	if err != nil {
		t.Fatalf("normalizeContainerWorkspace: %v", err)
	}
	foundRootMask := false
	for _, mount := range plan.mounts {
		if mount.kind == containerMountTmpfs && mount.target == filepath.Join(worktree, containerNodeModulesName) {
			foundRootMask = true
		}
		if mount.target == containerInputTarget {
			t.Fatalf("an empty InputDir must not add the fixed input mount: %#v", plan.mounts)
		}
	}
	if !foundRootMask {
		t.Fatalf("the root node_modules mask must exist even when the host directory is absent: %#v", plan.mounts)
	}
}

func TestContainerWorkspaceRejectsUnsafeOrOverlappingRoots(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, input := testContainerWorkspaceDirs(t)
	root := filepath.Dir(worktree)

	filePath := filepath.Join(root, "plain-file")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write plain file: %v", err)
	}
	symlinkPath := filepath.Join(root, "symlink")
	if err := os.Symlink(worktree, symlinkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	for _, directory := range []string{
		filepath.Join(worktree, ".git"),
		filepath.Join(worktree, "input"),
		filepath.Join(gitCommon, "input"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("create overlap fixture %s: %v", directory, err)
		}
	}
	socketDir, err := os.MkdirTemp("", "wb-sock-")
	if err != nil {
		t.Fatalf("create a short socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "s.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on test unix socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	cases := map[string]struct {
		workspace ContainerWorkspace
		errorPart string
	}{
		"empty worktree":    {ContainerWorkspace{GitCommonDir: gitCommon}, "empty"},
		"relative worktree": {ContainerWorkspace{WorktreeDir: "worktree", GitCommonDir: gitCommon}, "not absolute"},
		"unclean worktree":  {ContainerWorkspace{WorktreeDir: worktree + "/../worktree", GitCommonDir: gitCommon}, "clean path"},
		"missing worktree":  {ContainerWorkspace{WorktreeDir: filepath.Join(root, "absent"), GitCommonDir: gitCommon}, "inspect"},
		"regular file root": {ContainerWorkspace{WorktreeDir: filePath, GitCommonDir: gitCommon}, "not a directory"},
		"symlink root":      {ContainerWorkspace{WorktreeDir: symlinkPath, GitCommonDir: gitCommon}, "symlink"},
		"socket root":       {ContainerWorkspace{WorktreeDir: socketPath, GitCommonDir: gitCommon}, "socket"},
		"filesystem root":   {ContainerWorkspace{WorktreeDir: "/", GitCommonDir: gitCommon}, "forbidden"},
		"owner home":        {ContainerWorkspace{WorktreeDir: "/home/testowner", GitCommonDir: gitCommon}, "forbidden"},
		"home ancestor":     {ContainerWorkspace{WorktreeDir: "/home", GitCommonDir: gitCommon}, "forbidden"},
		"projects root":     {ContainerWorkspace{WorktreeDir: "/home/testowner/projects", GitCommonDir: gitCommon}, "forbidden"},
		"worktrees root":    {ContainerWorkspace{WorktreeDir: "/home/testowner/.worktrees", GitCommonDir: gitCommon}, "forbidden"},
		"run store root":    {ContainerWorkspace{WorktreeDir: "/home/testowner/.wb", GitCommonDir: gitCommon}, "forbidden"},
		"comma in path":     {ContainerWorkspace{WorktreeDir: worktree + ",x", GitCommonDir: gitCommon}, "unsafe character"},
		"colon in path":     {ContainerWorkspace{WorktreeDir: worktree + ":x", GitCommonDir: gitCommon}, "unsafe character"},
		"quote in path":     {ContainerWorkspace{WorktreeDir: worktree + `"x`, GitCommonDir: gitCommon}, "unsafe character"},
		"control in path":   {ContainerWorkspace{WorktreeDir: worktree + "\x01", GitCommonDir: gitCommon}, "control character"},
		"overlapping git":   {ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: filepath.Join(worktree, ".git")}, "overlaps"},
		"overlapping input": {ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon, InputDir: filepath.Join(worktree, "input")}, "overlaps"},
		"input inside git":  {ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon, InputDir: filepath.Join(gitCommon, "input")}, "overlaps"},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeContainerWorkspace(&testCase.workspace, testContainerWorkspaceHost())
			if err == nil {
				t.Fatalf("workspace %#v must be refused", testCase.workspace)
			}
			if !strings.Contains(err.Error(), testCase.errorPart) {
				t.Fatalf("error = %v, want it to contain %q", err, testCase.errorPart)
			}
		})
	}

	// The legitimate worktree below the projects tree is not the whole home.
	valid := &ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon, InputDir: input}
	if _, err := normalizeContainerWorkspace(valid, testContainerWorkspaceHost()); err != nil {
		t.Fatalf("a legitimate worktree below the projects tree must be accepted: %v", err)
	}
}

func TestContainerWorkspaceRefusesSymlinkedDependencyMaskTargets(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, _ := testContainerWorkspaceDirs(t)
	target := filepath.Join(worktree, "node_modules")
	if err := os.Symlink("/host/dependencies", target); err != nil {
		t.Fatalf("create symlinked node_modules: %v", err)
	}
	_, err := normalizeContainerWorkspace(&ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon}, testContainerWorkspaceHost())
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want a symlinked dependency mask refusal", err)
	}
}

func TestContainerRuntimeVerifiesOnlyTheExactPlannedMounts(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	worktree, gitCommon, input := testContainerWorkspaceDirs(t)
	workspace := &ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon, InputDir: input}
	plan := testContainerWorkspacePlan(t, testContainerWorkspaceOptions("", newFakeContainerDocker(), workspace), workspace)
	maskTarget := ""
	for _, planned := range plan.mounts {
		if planned.kind == containerMountTmpfs {
			maskTarget = planned.target
			break
		}
	}
	if maskTarget == "" {
		t.Fatal("the exact mount fixture needs at least one planned tmpfs mask")
	}

	exact := func(t *testing.T) containerInspectDocument {
		t.Helper()
		raw := testContainerWorkspaceDocument(t, testContainerID, "wb-agent", testContainerRunID, "nonce", testContainerRuntimeOwner, policy, plan.mounts)
		var documents []containerInspectDocument
		if err := json.Unmarshal(raw, &documents); err != nil {
			t.Fatalf("decode exact mount fixture: %v", err)
		}
		return documents[0]
	}

	cases := map[string]struct {
		mutate func(*containerInspectDocument)
	}{
		"exact plan": {mutate: func(*containerInspectDocument) {}},
		"extra bind": {mutate: func(document *containerInspectDocument) {
			document.Mounts = append(document.Mounts, containerInspectMount{Type: "bind", Source: "/extra", Destination: "/extra", RW: true})
		}},
		"wrong read-write flag": {mutate: func(document *containerInspectDocument) {
			document.Mounts[0].RW = false
		}},
		"wrong source": {mutate: func(document *containerInspectDocument) {
			document.Mounts[0].Source = "/somewhere/else"
		}},
		"wrong destination": {mutate: func(document *containerInspectDocument) {
			document.Mounts[1].Destination = "/wrong"
		}},
		"socket mount": {mutate: func(document *containerInspectDocument) {
			document.Mounts = append(document.Mounts, containerInspectMount{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock", RW: true})
		}},
		"home mount": {mutate: func(document *containerInspectDocument) {
			document.Mounts = append(document.Mounts, containerInspectMount{Type: "bind", Source: "/home/testowner", Destination: "/home", RW: false})
		}},
		"unplanned tmpfs": {mutate: func(document *containerInspectDocument) {
			document.Mounts = append(document.Mounts, containerInspectMount{Type: "tmpfs", Destination: "/tmp/wb-extra", RW: true})
			document.HostConfig.Tmpfs["/tmp/wb-extra"] = containerTmpfsOptions
		}},
		"missing planned bind": {mutate: func(document *containerInspectDocument) {
			document.Mounts = document.Mounts[1:]
			document.HostConfig.Binds = document.HostConfig.Binds[1:]
		}},
		"wrong bind map": {mutate: func(document *containerInspectDocument) {
			document.HostConfig.Binds[0] = "/wrong:/wrong:rw"
		}},
		"extra named volume": {mutate: func(document *containerInspectDocument) {
			document.HostConfig.Volumes = map[string]json.RawMessage{"/cache": json.RawMessage("{}")}
		}},
		"host network": {mutate: func(document *containerInspectDocument) {
			document.HostConfig.NetworkMode = "host"
		}},
		"host pid namespace": {mutate: func(document *containerInspectDocument) {
			document.HostConfig.PidMode = "host"
		}},
		"host ipc namespace": {mutate: func(document *containerInspectDocument) {
			document.HostConfig.IpcMode = "host"
		}},
		"added capability despite cap-drop all": {mutate: func(document *containerInspectDocument) {
			document.HostConfig.CapAdd = []string{"SYS_ADMIN"}
		}},
		"unexpected tmpfs source": {mutate: func(document *containerInspectDocument) {
			document.Mounts = append(document.Mounts, containerInspectMount{
				Type:        string(containerMountTmpfs),
				Source:      "/host/dependencies",
				Destination: maskTarget,
				RW:          true,
			})
		}},
		"duplicate top-level tmpfs": {mutate: func(document *containerInspectDocument) {
			planned := containerInspectMount{Type: string(containerMountTmpfs), Destination: maskTarget, RW: true}
			document.Mounts = append(document.Mounts, planned, planned)
		}},
		"unknown mount kind": {mutate: func(document *containerInspectDocument) {
			document.Mounts = append(document.Mounts, containerInspectMount{Type: "volume", Source: "wb-cache", Destination: "/cache", RW: true})
		}},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			document := exact(t)
			testCase.mutate(&document)
			err := verifyContainerHostConfig(policy, document, plan.mounts)
			if name == "exact plan" {
				if err != nil {
					t.Fatalf("the exact planned mount allowlist must verify: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("mutated mount plan %q must be refused", name)
			}
		})
	}
}

func TestContainerTmpfsOptionsAreTheClosedExecutableSet(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		containerTmpfsOptions,
		"exec,mode=" + containerTmpfsMode + ",nodev,nosuid,rw",
	} {
		if err := verifyContainerTmpfsOptions(testContainerID, "/tmp/mask", raw); err != nil {
			t.Fatalf("the fixed executable tmpfs option set %q must verify: %v", raw, err)
		}
	}
	refused := map[string]string{
		"empty":              "",
		"blank":              " ",
		"missing mode":       "rw,nosuid,nodev,exec",
		"missing exec":       "rw,nosuid,nodev,mode=" + containerTmpfsMode,
		"missing nodev":      "rw,nosuid,exec,mode=" + containerTmpfsMode,
		"missing nosuid":     "rw,nodev,exec,mode=" + containerTmpfsMode,
		"missing rw":         "nosuid,nodev,exec,mode=" + containerTmpfsMode,
		"noexec default":     "rw,nosuid,nodev,noexec,mode=" + containerTmpfsMode,
		"contradictory ro":   "ro,nosuid,nodev,exec,mode=" + containerTmpfsMode,
		"contradictory suid": "rw,suid,nodev,exec,mode=" + containerTmpfsMode,
		"contradictory dev":  "rw,nosuid,dev,exec,mode=" + containerTmpfsMode,
		"wrong mode":         "rw,nosuid,nodev,exec,mode=0755",
		"unplanned size":     "rw,nosuid,nodev,exec,mode=" + containerTmpfsMode + ",size=1g",
		"duplicate":          "rw,rw,nosuid,nodev,exec,mode=" + containerTmpfsMode,
		"trailing comma":     "rw,nosuid,nodev,exec,mode=" + containerTmpfsMode + ",",
	}
	for name, raw := range refused {
		name, raw := name, raw
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := verifyContainerTmpfsOptions(testContainerID, "/tmp/mask", raw); err == nil {
				t.Fatalf("tmpfs options %q must be refused", raw)
			}
		})
	}
}

func TestContainerRuntimeRefusesTmpfsMasksThatAreNotTheFixedExecutableSet(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	worktree, gitCommon, input := testContainerWorkspaceDirs(t)
	workspace := &ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon, InputDir: input}
	plan := testContainerWorkspacePlan(t, testContainerWorkspaceOptions("", newFakeContainerDocker(), workspace), workspace)
	mask := filepath.Join(worktree, containerNodeModulesName)
	cases := map[string]struct {
		mutate    func(*containerInspectDocument)
		errorPart string
	}{
		"missing mask entry": {mutate: func(document *containerInspectDocument) {
			delete(document.HostConfig.Tmpfs, mask)
		}, errorPart: "missing the planned tmpfs"},
		"empty options": {mutate: func(document *containerInspectDocument) {
			document.HostConfig.Tmpfs[mask] = ""
		}, errorPart: "without options"},
		"default noexec options": {mutate: func(document *containerInspectDocument) {
			document.HostConfig.Tmpfs[mask] = "rw,nosuid,nodev,mode=" + containerTmpfsMode
		}, errorPart: "exec"},
		"unplanned option": {mutate: func(document *containerInspectDocument) {
			document.HostConfig.Tmpfs[mask] = containerTmpfsOptions + ",size=1g"
		}, errorPart: "unplanned option"},
		"duplicate option": {mutate: func(document *containerInspectDocument) {
			document.HostConfig.Tmpfs[mask] = containerTmpfsOptions + ",rw"
		}, errorPart: "duplicate option"},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			document := testContainerWorkspaceInspect(t, policy, plan)
			testCase.mutate(&document)
			err := verifyContainerHostConfig(policy, document, plan.mounts)
			if err == nil || !strings.Contains(err.Error(), testCase.errorPart) {
				t.Fatalf("mutated tmpfs mask %q error = %v, want it to contain %q", name, err, testCase.errorPart)
			}
		})
	}
}

// TestContainerRuntimeAcceptsTmpfsMasksReportedOnlyInHostConfig pins the
// daemon form this policy's --tmpfs arguments produce: the masks appear in
// HostConfig.Tmpfs and the top-level Mounts array carries only binds.
func TestContainerRuntimeAcceptsTmpfsMasksReportedOnlyInHostConfig(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	worktree, gitCommon, input := testContainerWorkspaceDirs(t)
	workspace := &ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon, InputDir: input}
	plan := testContainerWorkspacePlan(t, testContainerWorkspaceOptions("", newFakeContainerDocker(), workspace), workspace)
	document := testContainerWorkspaceInspect(t, policy, plan)
	masks := 0
	for _, mount := range document.Mounts {
		if mount.Type == string(containerMountTmpfs) {
			t.Fatalf("the default --tmpfs fixture must not invent a top-level tmpfs entry: %#v", mount)
		}
	}
	for _, planned := range plan.mounts {
		if planned.kind != containerMountTmpfs {
			continue
		}
		options, ok := document.HostConfig.Tmpfs[planned.target]
		if !ok || options != containerTmpfsOptions {
			t.Fatalf("HostConfig.Tmpfs[%q] = %q (present=%t), want the exact %q", planned.target, options, ok, containerTmpfsOptions)
		}
		masks++
	}
	if masks == 0 {
		t.Fatal("the fixture plan has no tmpfs mask")
	}
	if err := verifyContainerHostConfig(policy, document, plan.mounts); err != nil {
		t.Fatalf("a tmpfs mask reported only in HostConfig.Tmpfs must verify: %v", err)
	}
}

// TestContainerRuntimeAcceptsDockerSyntheticTmpfsSource models the separate,
// redundant form where a daemon additionally lists a tmpfs mask in the
// top-level Mounts array. That entry is optional, but when it exists it must
// still name the planned destination and a permitted synthetic source.
func TestContainerRuntimeAcceptsDockerSyntheticTmpfsSource(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	worktree, gitCommon, input := testContainerWorkspaceDirs(t)
	workspace := &ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon, InputDir: input}
	plan := testContainerWorkspacePlan(t, testContainerWorkspaceOptions("", newFakeContainerDocker(), workspace), workspace)
	document := testContainerWorkspaceInspect(t, policy, plan)
	appended := 0
	for _, planned := range plan.mounts {
		if planned.kind != containerMountTmpfs {
			continue
		}
		document.Mounts = append(document.Mounts, containerInspectMount{
			Type:        string(containerMountTmpfs),
			Source:      containerTmpfsSource,
			Destination: planned.target,
			RW:          planned.writable,
		})
		appended++
	}
	if appended == 0 {
		t.Fatal("the fixture plan has no tmpfs mask to report redundantly")
	}
	if err := verifyContainerHostConfig(policy, document, plan.mounts); err != nil {
		t.Fatalf("Docker's synthetic tmpfs source must verify: %v", err)
	}
}

func TestContainerHostConfigRefusesAnAddedCapabilityDespiteCapDropAll(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	worktree, gitCommon, input := testContainerWorkspaceDirs(t)
	workspace := &ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon, InputDir: input}
	plan := testContainerWorkspacePlan(t, testContainerWorkspaceOptions("", newFakeContainerDocker(), workspace), workspace)
	raw := testContainerWorkspaceDocument(t, testContainerID, "wb-agent", testContainerRunID, "nonce", testContainerRuntimeOwner, policy, plan.mounts)
	var documents []containerInspectDocument
	if err := json.Unmarshal(raw, &documents); err != nil {
		t.Fatalf("decode exact mount fixture: %v", err)
	}
	document := documents[0]
	if !containsFold(document.HostConfig.CapDrop, "ALL") {
		t.Fatalf("the fixture must model cap-drop ALL: %#v", document.HostConfig.CapDrop)
	}
	document.HostConfig.CapAdd = []string{"SYS_ADMIN"}
	err := verifyContainerHostConfig(policy, document, plan.mounts)
	if err == nil || !strings.Contains(err.Error(), "adds capabilities") {
		t.Fatalf("error = %v, want an added-capability refusal", err)
	}
}

func TestContainerWorkerCommandKeepsTheCredentialOutOfArgv(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, input := testContainerWorkspaceDirs(t)
	workspace := &ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon, InputDir: input}
	policy := executionTestPolicy()
	options := testContainerWorkspaceOptions(testContainerSlotStateDir(t), newFakeContainerDocker(), workspace)
	options.host.lookupEnv = func(name string) (string, bool) {
		switch name {
		case "HOME":
			return "/host/owner-home", true
		case "PATH":
			return "/host/bin", true
		case "DOCKER_HOST":
			return "tcp://docker.test:2376", true
		case "DOCKER_TLS_VERIFY":
			return "1", true
		case "HOST_SECRET":
			return "must-not-be-forwarded", true
		default:
			return "", false
		}
	}
	plan := testContainerWorkspacePlan(t, options, workspace)
	fake := newContainerWorkspaceReadyFake(t, policy, plan)
	options.commands = fake.commands()

	runtime, err := OpenContainerRuntime(context.Background(), options)
	if err != nil {
		t.Fatalf("OpenContainerRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()

	credential := Credential{EnvName: "DEEPSEEK_API_KEY", Value: "fake-provider-value"}
	command, err := runtime.WorkerCommand(context.Background(), credential, []string{"/usr/bin/env"})
	if err != nil {
		t.Fatalf("WorkerCommand: %v", err)
	}
	if command == nil || len(command.Args) < 5 {
		t.Fatalf("WorkerCommand returned no usable command: %#v", command)
	}
	wantPrefix := []string{"/test/docker", "exec", "--user", testContainerRuntimeOwner, "--workdir", worktree}
	if !reflect.DeepEqual(command.Args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("WorkerCommand prefix = %#v, want %#v", command.Args[:len(wantPrefix)], wantPrefix)
	}
	if command.Args[len(command.Args)-1] != "/usr/bin/env" {
		t.Fatalf("WorkerCommand argv tail = %#v, want the supplied argv unchanged", command.Args[len(command.Args)-1:])
	}
	joined := strings.Join(command.Args, "\x00")
	if strings.Contains(joined, credential.Value) {
		t.Fatal("the credential value must never enter Docker argv")
	}
	if strings.Contains(joined, "DEEPSEEK_API_KEY=fake-provider-value") {
		t.Fatal("the credential must be passed by name, never as an argv assignment")
	}
	if !strings.Contains(joined, "\x00--env\x00DEEPSEEK_API_KEY\x00") {
		t.Fatalf("WorkerCommand must pass the credential name alone: %#v", command.Args)
	}
	for _, entry := range containerWorkerEnvironment() {
		if !strings.Contains(joined, "\x00--env\x00"+entry+"\x00") {
			t.Fatalf("WorkerCommand is missing fixed worker env %q: %#v", entry, command.Args)
		}
	}
	if strings.Contains(joined, "HOME=/host/owner-home") || strings.Contains(joined, "PATH=/host/bin") {
		t.Fatal("host transport environment must not become worker environment")
	}

	environment := environmentMap(command.Env)
	if environment["DEEPSEEK_API_KEY"] != credential.Value {
		t.Fatal("the Docker client environment must carry the credential value")
	}
	if environment["DOCKER_HOST"] != "tcp://docker.test:2376" || environment["HOME"] != "/host/owner-home" {
		t.Fatalf("the captured transport must reach the Docker client: %#v", command.Env)
	}
	if _, present := environment["HOST_SECRET"]; present {
		t.Fatalf("a host secret outside the transport allowlist was forwarded: %#v", command.Env)
	}
	if environment["PATH"] != "/host/bin" {
		t.Fatalf("the transport PATH was not retained: %#v", command.Env)
	}
	if strings.Contains(fmt.Sprintf("%#v", runtime), credential.Value) {
		t.Fatal("the credential value must not be stored on the runtime")
	}

	credentialFree, err := runtime.WorkerCommand(context.Background(), Credential{}, []string{"/usr/bin/validate"})
	if err != nil {
		t.Fatalf("credential-free WorkerCommand: %v", err)
	}
	if strings.Contains(strings.Join(credentialFree.Args, "\x00"), "DEEPSEEK_API_KEY") {
		t.Fatalf("a credential-free validation command must not name a provider env: %#v", credentialFree.Args)
	}
	if _, present := environmentMap(credentialFree.Env)["DEEPSEEK_API_KEY"]; present {
		t.Fatal("a credential-free validation command must not carry the credential")
	}

	for name, invalid := range map[string]Credential{
		"reserved transport name": {EnvName: "PATH", Value: "x"},
		"reserved docker name":    {EnvName: "DOCKER_HOST", Value: "x"},
		"reserved worker name":    {EnvName: "NX_DAEMON", Value: "x"},
		"lowercase name":          {EnvName: "deepseek_api_key", Value: "x"},
		"value without name":      {Value: "x"},
		"name without value":      {EnvName: "DEEPSEEK_API_KEY"},
		"nul in value":            {EnvName: "DEEPSEEK_API_KEY", Value: "a\x00b"},
	} {
		name, invalid := name, invalid
		t.Run(name, func(t *testing.T) {
			if command, err := runtime.WorkerCommand(context.Background(), invalid, []string{"/bin/true"}); err == nil {
				t.Fatalf("invalid credential %#v must be refused; command = %#v", invalid, command)
			}
		})
	}
}

func TestContainerRuntimePrepareWorkerHomeIsUnprivilegedAndBounded(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, _ := testContainerWorkspaceDirs(t)
	workspace := &ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon}
	policy := executionTestPolicy()
	options := testContainerWorkspaceOptions(testContainerSlotStateDir(t), newFakeContainerDocker(), workspace)
	// Linux UID 1001 must work exactly like the macOS 501 fixture.
	options.host.currentUser = func() (int, int) { return 1001, 1001 }
	plan := testContainerWorkspacePlan(t, options, workspace)
	fake := newContainerWorkspaceReadyFake(t, policy, plan)
	options.commands = fake.commands()

	runtime, err := OpenContainerRuntime(context.Background(), options)
	if err != nil {
		t.Fatalf("OpenContainerRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()

	if err := runtime.PrepareWorkerHome(context.Background()); err != nil {
		t.Fatalf("PrepareWorkerHome: %v", err)
	}
	var prepareCall []string
	prepareIndex := -1
	cgroupIndex := -1
	fake.mutex.Lock()
	for index, call := range fake.calls {
		if len(call) >= 2 && call[0] == "exec" && call[1] == "--user" {
			prepareCall = call
			prepareIndex = index
		}
		if len(call) >= 3 && call[0] == "exec" && call[1] == runtime.ContainerID() && call[2] == "cat" {
			cgroupIndex = index
		}
	}
	fake.mutex.Unlock()
	if prepareCall == nil {
		t.Fatal("PrepareWorkerHome did not run an unprivileged docker exec")
	}
	if cgroupIndex < 0 || prepareIndex < cgroupIndex {
		t.Fatalf("worker home preparation must follow the cgroup verification (cgroup call %d, prepare call %d)", cgroupIndex, prepareIndex)
	}
	if prepareCall[2] != "1001:1001" {
		t.Fatalf("prepare --user = %q, want 1001:1001", prepareCall[2])
	}
	if prepareCall[3] != "--workdir" || prepareCall[4] != "/tmp" {
		t.Fatalf("prepare workdir = %#v, want the fixed /tmp", prepareCall[3:5])
	}
	scriptIndex := -1
	for index, argument := range prepareCall {
		if argument == "-c" {
			scriptIndex = index + 1
		}
	}
	if scriptIndex < 0 || scriptIndex >= len(prepareCall) {
		t.Fatalf("prepare call carries no shell script: %#v", prepareCall)
	}
	script := prepareCall[scriptIndex]
	for _, required := range []string{
		"umask 077",
		"mkdir -p /tmp/wb-agent-home /tmp/wb-dsh-home/profiles/headless",
		"chmod 700 /tmp/wb-agent-home /tmp/wb-dsh-home",
		"cp -R /opt/wb-dsh-profile/. /tmp/wb-dsh-home/profiles/headless/",
		"test -d /opt/wb-dsh-profile",
		"test -d /opt/agent/node_modules",
		"if [ -L \"$path\" ]",
		"command -v dsh",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("prepare script is missing %q:\n%s", required, script)
		}
	}
	for _, forbidden := range []string{"cp -a", "cp -p", "cp --preserve", "chown", "chmod 777", "--privileged", "/host/"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("prepare script contains forbidden ownership-preserving or host operation %q:\n%s", forbidden, script)
		}
	}
	for _, entry := range []string{"HOME=/tmp/wb-agent-home", "DSH_HOME=/tmp/wb-dsh-home"} {
		if !strings.Contains(strings.Join(prepareCall, "\x00"), "\x00--env\x00"+entry+"\x00") {
			t.Fatalf("prepare exec does not set %s: %#v", entry, prepareCall)
		}
	}

	// An image that lacks the template/tool must surface as a closed failure.
	fake.mutex.Lock()
	fake.execErr = &containerDockerError{Args: prepareCall, Stderr: "the template is missing", Err: fmt.Errorf("exit status 1")}
	fake.mutex.Unlock()
	if err := runtime.PrepareWorkerHome(context.Background()); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("a failed preparation must fail closed: %v", err)
	}
}

func TestContainerWorkspaceNilLifecycleKeepsNoHostMounts(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	stateDir := testContainerSlotStateDir(t)
	fake := newReadyFake(t, policy)
	options := testContainerRuntimeOptions(stateDir, fake)

	runtime, err := OpenContainerRuntime(context.Background(), options)
	if err != nil {
		t.Fatalf("OpenContainerRuntime without a workspace: %v", err)
	}
	createArgs := fake.createCall(t)
	for _, argument := range createArgs {
		if argument == "--mount" || argument == "--volume" || argument == "-v" {
			t.Fatalf("a workspace-free lifecycle probe must plan no host mounts: %v", createArgs)
		}
	}
	if _, err := runtime.WorkerCommand(context.Background(), Credential{}, []string{"/bin/true"}); err == nil {
		t.Fatal("WorkerCommand without a workspace must fail closed instead of inventing a workdir")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestContainerTransportEnvironmentIsABoundedAllowlist(t *testing.T) {
	t.Parallel()
	lookup := func(name string) (string, bool) {
		values := map[string]string{
			"HOME":              "/host/home",
			"PATH":              "/host/bin",
			"DOCKER_HOST":       "unix:///host/docker.sock",
			"DOCKER_TLS_VERIFY": "1",
			"HOST_SECRET":       "must-not-appear",
			"DEEPSEEK_API_KEY":  "must-not-appear",
		}
		value, ok := values[name]
		return value, ok
	}
	environment := containerTransportEnvironment(lookup)
	values := environmentMap(environment)
	if values["HOME"] != "/host/home" || values["PATH"] != "/host/bin" || values["DOCKER_HOST"] == "" || values["DOCKER_TLS_VERIFY"] != "1" {
		t.Fatalf("allowlisted transport values are missing: %#v", environment)
	}
	for _, forbidden := range []string{"HOST_SECRET", "DEEPSEEK_API_KEY"} {
		if _, present := values[forbidden]; present {
			t.Fatalf("transport allowlist forwarded %s: %#v", forbidden, environment)
		}
	}
	if environment == nil {
		t.Fatal("an empty transport must be a non-nil empty environment")
	}
	if empty := containerTransportEnvironment(nil); empty == nil || environmentMap(empty)["PATH"] == "" {
		t.Fatalf("a missing lookup must still produce a non-nil PATH fallback environment: %#v", empty)
	}
	// A missing PATH is filled so the Docker client itself remains resolvable.
	noPath := containerTransportEnvironment(func(name string) (string, bool) {
		if name == "HOME" {
			return "/host/home", true
		}
		return "", false
	})
	if environmentMap(noPath)["PATH"] != defaultPath {
		t.Fatalf("missing transport PATH = %#v, want %q", noPath, defaultPath)
	}
}

func TestContainerWorkerEnvironmentIsStaticLinuxAndCacheFree(t *testing.T) {
	t.Parallel()
	values := environmentMap(containerWorkerEnvironment())
	for name, want := range map[string]string{
		"HOME":               containerWorkerHomeDir,
		"DSH_HOME":           containerWorkerDshHomeDir,
		"TMPDIR":             containerWorkerTmpDir,
		"NX_DAEMON":          "false",
		"RAYON_NUM_THREADS":  "1",
		"NX_ISOLATE_PLUGINS": "false",
	} {
		if values[name] != want {
			t.Fatalf("worker environment %s = %q, want %q", name, values[name], want)
		}
	}
	for _, cache := range []string{"GOCACHE", "GOMODCACHE", "GOPATH", "XDG_CACHE_HOME"} {
		if !strings.HasPrefix(values[cache], "/tmp/") {
			t.Fatalf("worker cache %s = %q, want a fixed /tmp location", cache, values[cache])
		}
	}
	if !strings.Contains(values["PATH"], "/opt/agent/node_modules/.bin") || !strings.Contains(values["PATH"], containerWorkerToolWrapperDir) {
		t.Fatalf("worker PATH = %q, want the image tool tree and p1 wrapper", values["PATH"])
	}
	if strings.Contains(values["PATH"], "/Users/") || strings.Contains(values["PATH"], "/home/") {
		t.Fatalf("worker PATH leaks a host directory: %q", values["PATH"])
	}
	// Static function results are independent copies.
	first := containerWorkerEnvironment()
	first[0] = "PATH=/tampered"
	if containerWorkerEnvironment()[0] == "PATH=/tampered" {
		t.Fatal("worker environment must not expose shared mutable state")
	}
}
