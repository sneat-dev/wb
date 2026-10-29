//go:build linux

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const testContainerRuntimeImageID = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

const testContainerRuntimeOwner = "501:20"

// fakeContainerDocker is the per-instance seam substitute: it records every
// argv, answers Docker's bounded metadata commands from a small map and models
// removal by forgetting the document.
type fakeContainerDocker struct {
	mutex sync.Mutex

	calls            [][]string
	imageInspect     []byte
	imageErr         error
	contextName      string
	contextErr       error
	daemonID         string
	daemonErr        error
	onInfo           func()
	inspectDocuments map[string][]byte
	inspectErrors    map[string]error
	onInspect        func(identity string)
	createID         string
	createErr        error
	onCreate         func(args []string, containerID string)
	execOutput       []byte
	execErr          error
	removeErr        error
	onRemove         func(containerID string)
	removed          []string

	attachErr     error
	attachFactory func(ctx context.Context, readEnd *os.File) (containerAttachProcess, error)
	attached      []string
	lastAttach    *testAttachProcess
}

func newFakeContainerDocker() *fakeContainerDocker {
	return &fakeContainerDocker{
		inspectDocuments: map[string][]byte{},
		inspectErrors:    map[string]error{},
		contextName:      testContainerContext,
		daemonID:         testContainerDaemonID,
	}
}

func (fake *fakeContainerDocker) commands() *containerCommands {
	return &containerCommands{command: fake.run, attach: fake.attach}
}

func (fake *fakeContainerDocker) run(_ context.Context, args []string) ([]byte, error) {
	fake.mutex.Lock()
	fake.calls = append(fake.calls, append([]string(nil), args...))
	switch args[0] {
	case "context":
		output, failure := fake.contextName, fake.contextErr
		fake.mutex.Unlock()
		return []byte(output + "\n"), failure
	case "info":
		hook := fake.onInfo
		if hook != nil {
			fake.mutex.Unlock()
			hook()
			fake.mutex.Lock()
		}
		output, failure := fake.daemonID, fake.daemonErr
		fake.mutex.Unlock()
		return []byte(output + "\n"), failure
	case "image":
		output, failure := fake.imageInspect, fake.imageErr
		fake.mutex.Unlock()
		return output, failure
	case "inspect":
		identity := args[1]
		hook := fake.onInspect
		if hook != nil {
			// The hook runs before the answer is read so a test can make a later
			// inspection observe a changed daemon answer without a sleep.
			fake.mutex.Unlock()
			hook(identity)
			fake.mutex.Lock()
		}
		document, found := fake.inspectDocuments[identity]
		failure := fake.inspectErrors[identity]
		fake.mutex.Unlock()
		if failure != nil {
			return nil, failure
		}
		if !found {
			return nil, testContainerNoSuchObject(identity)
		}
		return document, nil
	case "create":
		containerID, hook, failure := fake.createID, fake.onCreate, fake.createErr
		fake.mutex.Unlock()
		if hook != nil {
			hook(args, containerID)
		}
		return []byte(containerID + "\n"), failure
	case "exec":
		output, failure := fake.execOutput, fake.execErr
		fake.mutex.Unlock()
		return output, failure
	case "rm":
		containerID, failure, hook := args[len(args)-1], fake.removeErr, fake.onRemove
		fake.mutex.Unlock()
		if hook != nil {
			hook(containerID)
		}
		fake.recordRemoval(containerID)
		if failure == nil {
			fake.forgetContainer(containerID)
		}
		return nil, failure
	default:
		fake.mutex.Unlock()
		return nil, fmt.Errorf("unexpected docker command %v", args)
	}
}

func (fake *fakeContainerDocker) attach(ctx context.Context, containerID string, readEnd *os.File) (containerAttachProcess, error) {
	fake.mutex.Lock()
	fake.attached = append(fake.attached, containerID)
	failure, factory := fake.attachErr, fake.attachFactory
	fake.mutex.Unlock()
	if failure != nil {
		return nil, failure
	}
	if factory != nil {
		return factory(ctx, readEnd)
	}
	process := newTestAttachProcess(ctx, readEnd)
	fake.mutex.Lock()
	fake.lastAttach = process
	fake.mutex.Unlock()
	return process, nil
}

func (fake *fakeContainerDocker) setDocument(identity string, document []byte) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.inspectDocuments[identity] = document
}

func (fake *fakeContainerDocker) recordRemoval(containerID string) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.removed = append(fake.removed, containerID)
}

func (fake *fakeContainerDocker) forgetContainer(containerID string) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	for identity, raw := range fake.inspectDocuments {
		// Inspect answers are Docker's one-element array, so removal matches the
		// recorded document only after decoding that same shape.
		var documents []containerInspectDocument
		if err := json.Unmarshal(raw, &documents); err == nil {
			matches := false
			for _, document := range documents {
				if document.ID == containerID {
					matches = true
					break
				}
			}
			if matches {
				delete(fake.inspectDocuments, identity)
				continue
			}
		}
		if identity == containerID {
			delete(fake.inspectDocuments, identity)
		}
	}
}

func (fake *fakeContainerDocker) createCall(t *testing.T) []string {
	t.Helper()
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	for _, call := range fake.calls {
		if call[0] == "create" {
			return call
		}
	}
	t.Fatal("no docker create call was recorded")
	return nil
}

func (fake *fakeContainerDocker) createCount() int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	count := 0
	for _, call := range fake.calls {
		if call[0] == "create" {
			count++
		}
	}
	return count
}

func (fake *fakeContainerDocker) callCount(verb string) int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	count := 0
	for _, call := range fake.calls {
		if call[0] == verb {
			count++
		}
	}
	return count
}

func (fake *fakeContainerDocker) removedIDs() []string {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]string(nil), fake.removed...)
}

// inspectsBeforeFirstCreate reports the identities inspected after the last rm
// that preceded the first create. That window is exactly where recovery must
// prove the prior identity absent before it admits a new container.
func (fake *fakeContainerDocker) inspectsBeforeFirstCreate() []string {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	firstCreate := len(fake.calls)
	for index, call := range fake.calls {
		if call[0] == "create" {
			firstCreate = index
			break
		}
	}
	lastRemove := -1
	for index, call := range fake.calls[:firstCreate] {
		if call[0] == "rm" {
			lastRemove = index
		}
	}
	var identities []string
	for _, call := range fake.calls[lastRemove+1 : firstCreate] {
		if call[0] == "inspect" {
			identities = append(identities, call[1])
		}
	}
	return identities
}

func (fake *fakeContainerDocker) attachedIDs() []string {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]string(nil), fake.attached...)
}

func (fake *fakeContainerDocker) attachProcess() *testAttachProcess {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return fake.lastAttach
}

// testAttachProcess models the real attach client: Wait blocks until the
// controller cancels it, or until the inherited read end sees EOF because the
// controller closed the only write end.
type testAttachProcess struct {
	done   chan error
	eof    chan bool
	killed chan struct{}
	once   sync.Once
	waited atomic.Bool
}

func newTestAttachProcess(ctx context.Context, readEnd *os.File) *testAttachProcess {
	process := &testAttachProcess{
		done:   make(chan error, 1),
		eof:    make(chan bool, 1),
		killed: make(chan struct{}),
	}
	// The real child inherits the read end; the parent then closes its copy.
	// Duplicating here models that inheritance without racing the close.
	fd, err := syscall.Dup(int(readEnd.Fd()))
	if err != nil {
		process.done <- err
		return process
	}
	reader := os.NewFile(uintptr(fd), "test-attach-stdin")
	go func() {
		defer func() { _ = reader.Close() }()
		readResult := make(chan bool, 1)
		go func() {
			buffer := make([]byte, 1)
			_, readErr := reader.Read(buffer)
			readResult <- errors.Is(readErr, io.EOF)
		}()
		select {
		case <-ctx.Done():
		case <-process.killed:
		case eof := <-readResult:
			select {
			case process.eof <- eof:
			default:
			}
		}
		process.done <- nil
	}()
	return process
}

func (process *testAttachProcess) Wait() error {
	err := <-process.done
	process.waited.Store(true)
	return err
}

func (process *testAttachProcess) Kill() error {
	process.once.Do(func() { close(process.killed) })
	return nil
}

func testContainerRuntimeOptions(stateDir string, fake *fakeContainerDocker) ContainerRuntimeOptions {
	return ContainerRuntimeOptions{
		Policy:   executionTestPolicy(),
		RunID:    testContainerRunID,
		Lifetime: time.Hour,
		stateDir: stateDir,
		commands: fake.commands(),
		host: &containerHost{
			dockerPath:  func() (string, error) { return "/test/docker", nil },
			currentUser: func() (int, int) { return 501, 20 },
		},
	}
}

// testContainerRuntimeDocument renders the shape real `docker inspect` prints:
// a JSON array containing exactly one object. Production unmarshals that array,
// so a fixture that emitted a bare object would not model Docker at all.
func testContainerRuntimeDocument(t *testing.T, containerID, name, runID, nonce, owner string, policy ExecutionPolicy, running bool) []byte {
	t.Helper()
	status := "exited"
	pid := 0
	if running {
		status = "running"
		pid = 4242
	}
	raw, err := json.Marshal([]any{map[string]any{
		"Id":   containerID,
		"Name": "/" + name,
		"Config": map[string]any{
			"User": "0:0",
			"Labels": map[string]string{
				containerLabelRunID: runID,
				containerLabelNonce: nonce,
				containerLabelUser:  owner,
			},
		},
		"HostConfig": map[string]any{
			"Memory":        policy.MemoryBytes,
			"MemorySwap":    policy.MemorySwapBytes,
			"NanoCpus":      int64(policy.CPUCount) * nanosPerCPU,
			"PidsLimit":     policy.PIDsLimit,
			"Privileged":    false,
			"AutoRemove":    false,
			"Binds":         []string{},
			"CapDrop":       []string{"ALL"},
			"SecurityOpt":   []string{"no-new-privileges:true"},
			"RestartPolicy": map[string]any{"Name": "no"},
			"PortBindings":  map[string]any{},
		},
		"State":  map[string]any{"Running": running, "Pid": pid, "Status": status},
		"Mounts": []any{},
	}})
	if err != nil {
		t.Fatalf("marshal container inspect fixture: %v", err)
	}
	return raw
}

func testContainerCgroupOutput(policy ExecutionPolicy) []byte {
	return []byte(fmt.Sprintf("%d\n0\n%d %d\n%d\n", policy.MemoryBytes, policy.CPUCount*100000, 100000, policy.PIDsLimit))
}

func testContainerCreateIdentity(t *testing.T, args []string) (name, runID, nonce, owner string) {
	t.Helper()
	for index := 1; index+1 < len(args); index++ {
		switch args[index] {
		case "--name":
			name = args[index+1]
		case "--label":
			key, value, found := strings.Cut(args[index+1], "=")
			if !found {
				continue
			}
			switch key {
			case containerLabelRunID:
				runID = value
			case containerLabelNonce:
				nonce = value
			case containerLabelUser:
				owner = value
			}
		}
	}
	if name == "" || runID == "" || nonce == "" || owner == "" {
		t.Fatalf("docker create call is missing its generated name/labels: %v", args)
	}
	return name, runID, nonce, owner
}

// newReadyFake wires the fake Docker for a successful open: the image verifies,
// create registers an identity-matching stopped/running document, and the
// cgroup exec reports exactly the policy.
func newReadyFake(t *testing.T, policy ExecutionPolicy) *fakeContainerDocker {
	t.Helper()
	fake := newFakeContainerDocker()
	fake.imageInspect = containerInspectJSON(t, dockerImageInspect{
		ID:          testContainerRuntimeImageID,
		OS:          "linux",
		RepoDigests: []string{executionTestImage},
	})
	fake.createID = testContainerID
	fake.execOutput = testContainerCgroupOutput(policy)
	fake.onCreate = func(args []string, containerID string) {
		name, runID, nonce, owner := testContainerCreateIdentity(t, args)
		fake.setDocument(containerID, testContainerRuntimeDocument(t, containerID, name, runID, nonce, owner, policy, true))
	}
	return fake
}

// seedTestContainerRuntimeReservation writes a prior holder's durable
// reservation the way the runtime itself does: Reserve with an empty container
// ID, RecordContainerID once Docker has returned one, then Close.
func seedTestContainerRuntimeReservation(t *testing.T, stateDir string, reservation ContainerReservation) {
	t.Helper()
	slot := openTestContainerSlot(t, stateDir)
	containerID := reservation.ContainerID
	reservation.ContainerID = ""
	if err := slot.Reserve(reservation); err != nil {
		t.Fatalf("Reserve prior reservation: %v", err)
	}
	if containerID != "" {
		if err := slot.RecordContainerID(containerID); err != nil {
			t.Fatalf("RecordContainerID prior reservation: %v", err)
		}
	}
	if err := slot.Close(); err != nil {
		t.Fatalf("close seeded slot: %v", err)
	}
}

// seedLegacyTestContainerRuntimeReservation writes the on-disk shape produced
// before reservations were bound to a Docker Engine identity. The slot itself
// accepts such a file so the runtime can refuse it actionably rather than
// treating it as stale.
func seedLegacyTestContainerRuntimeReservation(t *testing.T, stateDir string, reservation ContainerReservation) {
	t.Helper()
	reservation.DaemonID = ""
	raw, err := json.Marshal(reservation)
	if err != nil {
		t.Fatalf("marshal legacy reservation: %v", err)
	}
	path := filepath.Join(stateDir, containerSlotReservationFileName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write legacy reservation: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("protect legacy reservation: %v", err)
	}
}

func testContainerNoSuchObject(identity string) error {
	return &containerDockerError{
		Args:   []string{"inspect", identity},
		Stderr: "Error: No such object: " + identity,
		Err:    errors.New("exit status 1"),
	}
}

// testContainerMissingAfterRemove is Docker's exact answer when `rm --force`
// loses a race with an external removal of the named container.
func testContainerMissingAfterRemove(identity string) error {
	return &containerDockerError{
		Args:   []string{"rm", "--force", identity},
		Stderr: "Error response from daemon: No such container: " + identity,
		Err:    errors.New("exit status 1"),
	}
}

func requireContainerArgSequence(t *testing.T, args []string, sequence ...string) {
	t.Helper()
	for _, wanted := range sequence {
		found := false
		for _, arg := range args {
			if arg == wanted {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("docker create args %v are missing %q", args, wanted)
		}
	}
}

func TestContainerRuntimeCreatesVerifiedContainedExecution(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	stateDir := testContainerSlotStateDir(t)
	fake := newReadyFake(t, policy)

	runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
	if err != nil {
		t.Fatalf("OpenContainerRuntime: %v", err)
	}

	if got := runtime.ContainerID(); got != testContainerID {
		t.Fatalf("ContainerID = %q, want %q", got, testContainerID)
	}
	if got := runtime.ImageID(); got != testContainerRuntimeImageID {
		t.Fatalf("ImageID = %q, want %q", got, testContainerRuntimeImageID)
	}
	// VerifiedLimits are the bounds actually observed in the kernel's cgroup
	// files, not the create arguments. HostConfig.memory_swap is the total
	// memory-plus-swap limit the policy passes ("--memory-swap"), while the
	// observed no-swap limit is memory.swap.max = 0.
	if got, want := runtime.VerifiedLimits(), (ContainerVerifiedLimits{
		MemoryBytes:     policy.MemoryBytes,
		MemorySwapBytes: 0,
		CPUCount:        policy.CPUCount,
		PIDsLimit:       policy.PIDsLimit,
	}); got != want {
		t.Fatalf("VerifiedLimits = %#v, want %#v", got, want)
	}
	if got := runtime.VerifiedLimits().MemoryBytes; got != int64(8*1024*1024*1024) {
		t.Fatalf("verified memory bytes = %d, want the 8 GiB policy memory limit", got)
	}
	if got := runtime.VerifiedLimits().CPUCount; got != 2 {
		t.Fatalf("verified CPU count = %d, want 2", got)
	}
	if got := runtime.VerifiedLimits().PIDsLimit; got != 128 {
		t.Fatalf("verified PIDs limit = %d, want 128", got)
	}

	createArgs := fake.createCall(t)
	requireContainerArgSequence(t, createArgs,
		"create", "--init", "--interactive", "--memory", "--memory-swap", "--cpus", "--pids-limit",
		"--restart", "--cap-drop", "--security-opt", "--pull=never", "--entrypoint", "-c", containerPID1Script,
	)
	requireContainerArgSequence(t, createArgs, fmt.Sprintf("%d", policy.MemoryBytes), fmt.Sprintf("%d", policy.MemorySwapBytes),
		fmt.Sprintf("%d", policy.CPUCount), fmt.Sprintf("%d", policy.PIDsLimit), "no", "ALL", "no-new-privileges", "/bin/sh", "wb-pid1",
		"3600s", testContainerRuntimeImageID)
	pid1User := false
	for index := 0; index+1 < len(createArgs); index++ {
		if createArgs[index] == "--user" && createArgs[index+1] == "0:0" {
			pid1User = true
		}
	}
	if !pid1User {
		t.Fatalf("docker create must force PID1 to root: %v", createArgs)
	}
	if strings.Contains(strings.Join(createArgs, " "), executionTestImage) {
		t.Fatalf("create must use the resolved immutable config ID, not the policy reference: %v", createArgs)
	}
	imageIndex := -1
	for index, arg := range createArgs {
		if arg == testContainerRuntimeImageID {
			imageIndex = index
		}
	}
	if imageIndex < 2 || createArgs[imageIndex-1] != "/bin/sh" || createArgs[imageIndex-2] != "--entrypoint" {
		t.Fatalf("image must follow the fixed --entrypoint /bin/sh pair: %v", createArgs)
	}
	forbidden := map[string]bool{
		"--volume": true, "--mount": true, "--publish": true, "-p": true, "-v": true,
		"--env": true, "-e": true, "--network": true, "--pid": true, "--ipc": true,
		"--userns": true, "--privileged": true, "--device": true, "--cap-add": true, "--tmpfs": true,
	}
	for _, arg := range createArgs {
		if forbidden[arg] {
			t.Fatalf("docker create args contain forbidden option %q: %v", arg, createArgs)
		}
		if strings.Contains(arg, "docker.sock") {
			t.Fatalf("docker create args mount the Docker socket: %v", createArgs)
		}
	}
	name, runID, nonce, owner := testContainerCreateIdentity(t, createArgs)
	if runID != testContainerRunID {
		t.Fatalf("create run label = %q, want %q", runID, testContainerRunID)
	}
	if owner != testContainerRuntimeOwner {
		t.Fatalf("create owner label = %q, want %q", owner, testContainerRuntimeOwner)
	}
	if len(nonce) != 32 || !containerSlotNoncePattern.MatchString(nonce) {
		t.Fatalf("create nonce label %q must be 32 lowercase hex characters", nonce)
	}
	if !containerSlotNamePattern.MatchString(name) {
		t.Fatalf("generated container name %q is not a safe bounded name", name)
	}
	if got := fake.attachedIDs(); !reflect.DeepEqual(got, []string{testContainerID}) {
		t.Fatalf("attach client IDs = %v, want the exact created container", got)
	}
	// The durable reservation binds the exact created container to the Engine
	// identity captured before create; nothing else from docker info is stored.
	durableRaw, err := os.ReadFile(filepath.Join(stateDir, containerSlotReservationFileName))
	if err != nil {
		t.Fatalf("read the durable reservation: %v", err)
	}
	var durable ContainerReservation
	if err := json.Unmarshal(durableRaw, &durable); err != nil {
		t.Fatalf("decode the durable reservation: %v", err)
	}
	if durable.ContainerID != testContainerID || durable.DaemonID != testContainerDaemonID {
		t.Fatalf("durable reservation = %#v, want container %s owned by daemon %s", durable, testContainerID, testContainerDaemonID)
	}

	execArgv, err := runtime.ExecArgv([]string{"/usr/local/bin/dsh", "--version"})
	if err != nil {
		t.Fatalf("ExecArgv: %v", err)
	}
	wantArgv := []string{"exec", "--user", testContainerRuntimeOwner, testContainerID, "/usr/local/bin/dsh", "--version"}
	if !reflect.DeepEqual(execArgv, wantArgv) {
		t.Fatalf("ExecArgv = %v, want %v", execArgv, wantArgv)
	}
	if _, err := runtime.ExecArgv(nil); err == nil {
		t.Fatal("ExecArgv must refuse an empty worker argv")
	}

	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := fake.removedIDs(); !reflect.DeepEqual(got, []string{testContainerID}) {
		t.Fatalf("removed containers = %v, want only %s", got, testContainerID)
	}
	slot, err := OpenContainerSlot(stateDir)
	if err != nil {
		t.Fatalf("OpenContainerSlot after Close: %v", err)
	}
	if reservation, ok := slot.Reservation(); ok {
		t.Fatalf("Close must clear the reservation after proving absence, still holds %#v", reservation)
	}
	if err := slot.Close(); err != nil {
		t.Fatalf("close verification slot: %v", err)
	}
	if _, err := runtime.ExecArgv([]string{"true"}); err == nil {
		t.Fatal("ExecArgv must refuse a closed runtime")
	}
}

func TestContainerRuntimePinsTheResolvedDockerContextForItsLifetime(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	stateDir := testContainerSlotStateDir(t)
	worktree, gitCommon, _ := testContainerWorkspaceDirs(t)
	workspace := &ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon}
	options := testContainerWorkspaceOptions(stateDir, newFakeContainerDocker(), workspace)
	plan := testContainerWorkspacePlan(t, options, workspace)
	fake := newContainerWorkspaceReadyFake(t, policy, plan)
	fake.contextName = "pinned-profile"
	options.commands = fake.commands()
	// Keep the host's own Docker variables out of this fixture so the resolved
	// context is the only authority being proved.
	options.host.lookupEnv = func(name string) (string, bool) {
		if name == "PATH" {
			return "/test/bin", true
		}
		return "", false
	}

	runtime, err := OpenContainerRuntime(context.Background(), options)
	if err != nil {
		t.Fatalf("OpenContainerRuntime: %v", err)
	}
	if got := fake.callCount("context"); got != 1 {
		t.Fatalf("context resolutions = %d, want exactly one", got)
	}
	// A context change elsewhere must not redirect the worker command: the
	// pinned transport still names the context resolved at open.
	fake.contextName = "switched-elsewhere"
	command, err := runtime.WorkerCommand(context.Background(), Credential{}, []string{"/bin/true"})
	if err != nil {
		t.Fatalf("WorkerCommand: %v", err)
	}
	if got := environmentMap(command.Env)["DOCKER_CONTEXT"]; got != "pinned-profile" {
		t.Fatalf("worker DOCKER_CONTEXT = %q, want the pinned %q", got, "pinned-profile")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := fake.callCount("context"); got != 1 {
		t.Fatalf("context was resolved again during later exec/cleanup: %d resolutions", got)
	}
}

func TestContainerRuntimeKeepsAnExplicitDockerHostTransportInsteadOfAContext(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	stateDir := testContainerSlotStateDir(t)
	worktree, gitCommon, _ := testContainerWorkspaceDirs(t)
	workspace := &ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon}
	options := testContainerWorkspaceOptions(stateDir, newFakeContainerDocker(), workspace)
	plan := testContainerWorkspacePlan(t, options, workspace)
	fake := newContainerWorkspaceReadyFake(t, policy, plan)
	options.commands = fake.commands()
	options.host.lookupEnv = func(name string) (string, bool) {
		switch name {
		case "DOCKER_HOST":
			return "tcp://docker.example:2376", true
		case "DOCKER_TLS_VERIFY":
			return "1", true
		case "DOCKER_CERT_PATH":
			return "/host/docker-certs", true
		case "PATH":
			return "/test/bin", true
		}
		return "", false
	}

	runtime, err := OpenContainerRuntime(context.Background(), options)
	if err != nil {
		t.Fatalf("OpenContainerRuntime: %v", err)
	}
	if got := fake.callCount("context"); got != 0 {
		t.Fatalf("an explicit DOCKER_HOST must not be replaced by a resolved context: %d context probes", got)
	}
	command, err := runtime.WorkerCommand(context.Background(), Credential{}, []string{"/bin/true"})
	if err != nil {
		t.Fatalf("WorkerCommand: %v", err)
	}
	environment := environmentMap(command.Env)
	if environment["DOCKER_HOST"] != "tcp://docker.example:2376" ||
		environment["DOCKER_TLS_VERIFY"] != "1" ||
		environment["DOCKER_CERT_PATH"] != "/host/docker-certs" {
		t.Fatalf("explicit DockerHost transport and TLS settings were not preserved: %#v", command.Env)
	}
	if _, present := environment["DOCKER_CONTEXT"]; present {
		t.Fatalf("an explicit DOCKER_HOST fixture must not gain a context override: %#v", command.Env)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestContainerRuntimeRefusesAnUnresolvableDockerContext(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	cases := map[string]func(*fakeContainerDocker){
		"context command fails": func(fake *fakeContainerDocker) {
			fake.contextErr = errors.New("docker context transport failed")
		},
		"invalid context name": func(fake *fakeContainerDocker) {
			fake.contextName = "--host"
		},
	}
	for name, configure := range cases {
		name, configure := name, configure
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stateDir := testContainerSlotStateDir(t)
			fake := newReadyFake(t, policy)
			configure(fake)
			options := testContainerRuntimeOptions(stateDir, fake)
			options.host.lookupEnv = func(string) (string, bool) { return "", false }

			runtime, err := OpenContainerRuntime(context.Background(), options)
			if err == nil {
				_ = runtime.Close()
				t.Fatalf("%s must refuse the execution", name)
			}
			if !strings.Contains(err.Error(), "context") {
				t.Fatalf("error = %v, want the unresolved Docker context named", err)
			}
			if fake.createCount() != 0 {
				t.Fatal("an unresolved context must block before create")
			}
			slot, openErr := OpenContainerSlot(stateDir)
			if openErr != nil {
				t.Fatalf("OpenContainerSlot: %v", openErr)
			}
			if _, ok := slot.Reservation(); ok {
				t.Fatal("a refusal before Reserve must not leave a reservation")
			}
			_ = slot.Close()
		})
	}
}

func TestContainerRuntimeRefusesCleanupWhenTheDaemonIdentityChanged(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	stateDir := testContainerSlotStateDir(t)
	fake := newReadyFake(t, policy)
	runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
	if err != nil {
		t.Fatalf("OpenContainerRuntime: %v", err)
	}
	// The daemon is replaced after the container was created. Cleanup must not
	// mistake the new daemon's view for an absence proof on the owner.
	fake.daemonID = testContainerDaemonID2
	closeErr := runtime.Close()
	if closeErr == nil {
		t.Fatal("Close must refuse cleanup on a different daemon")
	}
	if !strings.Contains(closeErr.Error(), "daemon") {
		t.Fatalf("Close error = %v, want an actionable daemon mismatch", closeErr)
	}
	// The diagnostic may only name the recorded daemon identity and the exact
	// durable reservation path; no transport state (DOCKER_HOST/context) was
	// stored, so claiming one would be a lie.
	for _, want := range []string{testContainerDaemonID, filepath.Join(stateDir, containerSlotReservationFileName)} {
		if !strings.Contains(closeErr.Error(), want) {
			t.Fatalf("Close error = %v, want %q named", closeErr, want)
		}
	}
	if strings.Contains(closeErr.Error(), "DOCKER_HOST") {
		t.Fatalf("Close error = %v must not claim a recorded DOCKER_HOST or context", closeErr)
	}
	if removed := fake.removedIDs(); len(removed) != 0 {
		t.Fatalf("a changed daemon must not remove anything: %v", removed)
	}
	slot, openErr := OpenContainerSlot(stateDir)
	if openErr != nil {
		t.Fatalf("OpenContainerSlot: %v", openErr)
	}
	reservation, ok := slot.Reservation()
	if !ok {
		t.Fatal("a changed daemon must retain the durable reservation")
	}
	if reservation.DaemonID != testContainerDaemonID {
		t.Fatalf("retained reservation daemon ID = %q, want the original %q", reservation.DaemonID, testContainerDaemonID)
	}
	_ = slot.Close()
}

func TestContainerRuntimeRefusesCleanupWhenTheDaemonIdentityCannotBeRead(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	stateDir := testContainerSlotStateDir(t)
	fake := newReadyFake(t, policy)
	runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
	if err != nil {
		t.Fatalf("OpenContainerRuntime: %v", err)
	}
	fake.daemonErr = errors.New("docker info transport failed")
	closeErr := runtime.Close()
	if closeErr == nil {
		t.Fatal("Close must refuse cleanup when the daemon identity cannot be confirmed")
	}
	if removed := fake.removedIDs(); len(removed) != 0 {
		t.Fatalf("an unreadable daemon must not remove anything: %v", removed)
	}
	slot, openErr := OpenContainerSlot(stateDir)
	if openErr != nil {
		t.Fatalf("OpenContainerSlot: %v", openErr)
	}
	if _, ok := slot.Reservation(); !ok {
		t.Fatal("an unreadable daemon during cleanup must retain the durable reservation")
	}
	_ = slot.Close()
}

func TestContainerRuntimeRefusesABusyGlobalSlotBeforeProbes(t *testing.T) {
	t.Parallel()
	stateDir := testContainerSlotStateDir(t)
	holder := openTestContainerSlot(t, stateDir)
	fake := newReadyFake(t, executionTestPolicy())

	runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
	if err == nil {
		_ = runtime.Close()
		t.Fatal("OpenContainerRuntime must refuse while another holder owns the global slot")
	}
	var busy *ContainerSlotBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("error = %v, want ContainerSlotBusyError", err)
	}
	if fake.createCount() != 0 {
		t.Fatal("a busy slot must refuse before docker create")
	}
	if err := holder.Close(); err != nil {
		t.Fatalf("close holder: %v", err)
	}
}

func TestContainerRuntimeRecoversOnlyAStoppedExactOwnedPriorContainer(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()

	t.Run("stopped exact owned container is removed and proven absent", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		seedTestContainerRuntimeReservation(t, stateDir, prior)
		fake := newReadyFake(t, policy)
		fake.setDocument(prior.ContainerID, testContainerRuntimeDocument(t, prior.ContainerID, prior.Name, prior.RunID, prior.Nonce, testContainerRuntimeOwner, policy, false))

		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err != nil {
			t.Fatalf("OpenContainerRuntime after recovery: %v", err)
		}
		if got := fake.removedIDs(); !reflect.DeepEqual(got, []string{prior.ContainerID}) {
			t.Fatalf("removed containers = %v, want the stopped prior %s", got, prior.ContainerID)
		}
		if fake.createCount() != 1 {
			t.Fatalf("create count = %d, want 1 after recovery", fake.createCount())
		}
		if runtime.ContainerID() != testContainerID {
			t.Fatalf("new ContainerID = %q, want %q", runtime.ContainerID(), testContainerID)
		}
		if err := runtime.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("missing exact object clears the stale reservation", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		seedTestContainerRuntimeReservation(t, stateDir, prior)
		fake := newReadyFake(t, policy)

		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err != nil {
			t.Fatalf("OpenContainerRuntime: %v", err)
		}
		if removed := fake.removedIDs(); len(removed) != 0 {
			t.Fatalf("an absent prior container must not be removed again: %v", removed)
		}
		if err := runtime.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("rm losing a race to an external removal still proves absence", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		seedTestContainerRuntimeReservation(t, stateDir, prior)
		fake := newReadyFake(t, policy)
		fake.setDocument(prior.ContainerID, testContainerRuntimeDocument(t, prior.ContainerID, prior.Name, prior.RunID, prior.Nonce, testContainerRuntimeOwner, policy, false))
		// Docker answers rm with its exact missing-container reply because the
		// documented prior container was removed between inspect and rm.
		fake.removeErr = testContainerMissingAfterRemove(prior.ContainerID)
		// The external removal only makes the prior container's own rm answer
		// missing. Clear that one-shot injection once it has fired so the later
		// cleanup of the newly created container sees a normal daemon.
		var raced atomic.Bool
		fake.onRemove = func(containerID string) {
			fake.forgetContainer(containerID)
			if containerID == prior.ContainerID && raced.CompareAndSwap(false, true) {
				fake.mutex.Lock()
				fake.removeErr = nil
				fake.mutex.Unlock()
			}
		}

		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err != nil {
			t.Fatalf("OpenContainerRuntime after the rm race: %v", err)
		}
		// The rm reply is not itself absence: the exact prior identity must be
		// inspected again after rm, and once more under the reservation clear,
		// before any new container is created.
		proved := fake.inspectsBeforeFirstCreate()
		wantProof := []string{prior.ContainerID, prior.ContainerID}
		if !reflect.DeepEqual(proved, wantProof) {
			t.Fatalf("absence proof before create inspected %v, want the ordered prior identity %v", proved, wantProof)
		}
		if fake.createCount() != 1 {
			t.Fatalf("create count = %d, want 1 after accepting the rm race", fake.createCount())
		}
		if runtime.ContainerID() != testContainerID {
			t.Fatalf("new ContainerID = %q, want the healthy new container %q", runtime.ContainerID(), testContainerID)
		}
		if err := runtime.Close(); err != nil {
			t.Fatalf("Close after the one-shot rm race: %v", err)
		}
	})

	t.Run("rm missing-container reply with a reappearing container retains the block", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		seedTestContainerRuntimeReservation(t, stateDir, prior)
		fake := newReadyFake(t, policy)
		fake.setDocument(prior.ContainerID, testContainerRuntimeDocument(t, prior.ContainerID, prior.Name, prior.RunID, prior.Nonce, testContainerRuntimeOwner, policy, false))
		// The rm reply claims the container is missing, but the following
		// inspect still finds it: the runtime must not treat the reply as proof.
		fake.removeErr = testContainerMissingAfterRemove(prior.ContainerID)

		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("a reappearing container must block despite the rm missing-container reply")
		}
		if !strings.Contains(err.Error(), "still present") {
			t.Fatalf("error = %v, want the failed absence proof named", err)
		}
		if fake.createCount() != 0 {
			t.Fatal("a reappearing container must block before create")
		}
		slot, openErr := OpenContainerSlot(stateDir)
		if openErr != nil {
			t.Fatalf("OpenContainerSlot: %v", openErr)
		}
		if _, ok := slot.Reservation(); !ok {
			t.Fatal("a failed absence proof must retain the durable reservation")
		}
		_ = slot.Close()
	})

	t.Run("rm failures other than the exact missing-container reply retain the block", func(t *testing.T) {
		t.Parallel()
		cases := map[string]func(identity string) error{
			"wrong identity": func(identity string) error {
				return testContainerMissingAfterRemove(testContainerID)
			},
			"broader text": func(identity string) error {
				return &containerDockerError{
					Args:   []string{"rm", "--force", identity},
					Stderr: "Error response from daemon: No such container: " + identity + "\nretry aborted",
					Err:    errors.New("exit status 1"),
				}
			},
			"inspect-shaped reply": func(identity string) error {
				return testContainerNoSuchObject(identity)
			},
			"daemon transport error": func(identity string) error {
				return &containerDockerError{
					Args: []string{"rm", "--force", identity},
					Err:  errors.New("connection reset by peer"),
				}
			},
			"generic error": func(string) error {
				return errors.New("docker daemon busy")
			},
		}
		for name, removeErr := range cases {
			name, removeErr := name, removeErr
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				stateDir := testContainerSlotStateDir(t)
				prior := testContainerReservation()
				prior.ContainerID = testContainerID2
				seedTestContainerRuntimeReservation(t, stateDir, prior)
				fake := newReadyFake(t, policy)
				fake.setDocument(prior.ContainerID, testContainerRuntimeDocument(t, prior.ContainerID, prior.Name, prior.RunID, prior.Nonce, testContainerRuntimeOwner, policy, false))
				fake.removeErr = removeErr(prior.ContainerID)

				runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
				if err == nil {
					_ = runtime.Close()
					t.Fatalf("OpenContainerRuntime must fail for %s", name)
				}
				if fake.createCount() != 0 {
					t.Fatalf("%s must block before create", name)
				}
				slot, openErr := OpenContainerSlot(stateDir)
				if openErr != nil {
					t.Fatalf("OpenContainerSlot: %v", openErr)
				}
				reservation, ok := slot.Reservation()
				if !ok {
					t.Fatalf("%s must retain the durable reservation", name)
				}
				if reservation.ContainerID != prior.ContainerID {
					t.Fatalf("retained reservation container ID = %q, want %q", reservation.ContainerID, prior.ContainerID)
				}
				_ = slot.Close()
			})
		}
	})

	t.Run("live prior container blocks without cleanup", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		seedTestContainerRuntimeReservation(t, stateDir, prior)
		fake := newReadyFake(t, policy)
		fake.setDocument(prior.ContainerID, testContainerRuntimeDocument(t, prior.ContainerID, prior.Name, prior.RunID, prior.Nonce, testContainerRuntimeOwner, policy, true))

		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("a live prior container must block a new execution")
		}
		if !strings.Contains(err.Error(), "still running") {
			t.Fatalf("error = %v, want an actionable still-running refusal", err)
		}
		if removed := fake.removedIDs(); len(removed) != 0 {
			t.Fatalf("a live prior container must never be removed: %v", removed)
		}
		if fake.createCount() != 0 {
			t.Fatal("a live prior container must block before create")
		}
		slot, openErr := OpenContainerSlot(stateDir)
		if openErr != nil {
			t.Fatalf("OpenContainerSlot: %v", openErr)
		}
		if _, ok := slot.Reservation(); !ok {
			t.Fatal("the prior reservation must be retained while the container is live")
		}
		_ = slot.Close()
	})

	t.Run("wrong nonce label blocks without removal", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		seedTestContainerRuntimeReservation(t, stateDir, prior)
		fake := newReadyFake(t, policy)
		fake.setDocument(prior.ContainerID, testContainerRuntimeDocument(t, prior.ContainerID, prior.Name, prior.RunID, strings.Repeat("f", 32), testContainerRuntimeOwner, policy, false))

		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("a nonce mismatch must refuse the prior container")
		}
		if removed := fake.removedIDs(); len(removed) != 0 {
			t.Fatalf("a container with a mismatched label must never be removed: %v", removed)
		}
		if fake.createCount() != 0 {
			t.Fatal("a mismatched prior must block before create")
		}
	})

	t.Run("wrong container ID blocks without removal", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		seedTestContainerRuntimeReservation(t, stateDir, prior)
		fake := newReadyFake(t, policy)
		fake.setDocument(prior.ContainerID, testContainerRuntimeDocument(t, testContainerID, prior.Name, prior.RunID, prior.Nonce, testContainerRuntimeOwner, policy, false))

		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("a container ID mismatch must refuse the prior container")
		}
		if removed := fake.removedIDs(); len(removed) != 0 {
			t.Fatalf("an ID-mismatched container must never be removed: %v", removed)
		}
	})

	t.Run("uncertain inspect retains the block", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		seedTestContainerRuntimeReservation(t, stateDir, prior)
		fake := newReadyFake(t, policy)
		fake.inspectErrors[prior.ContainerID] = errors.New("docker transport failed")

		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("an uncertain prior inspect must block")
		}
		if fake.createCount() != 0 {
			t.Fatal("an uncertain prior inspect must block before create")
		}
		if removed := fake.removedIDs(); len(removed) != 0 {
			t.Fatalf("an unverified prior container must not be removed: %v", removed)
		}
		slot, openErr := OpenContainerSlot(stateDir)
		if openErr != nil {
			t.Fatalf("OpenContainerSlot: %v", openErr)
		}
		if _, ok := slot.Reservation(); !ok {
			t.Fatal("an uncertain prior inspect must retain the reservation")
		}
		_ = slot.Close()
	})

	t.Run("a different daemon blocks without inspecting, removing or clearing", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		seedTestContainerRuntimeReservation(t, stateDir, prior)
		fake := newReadyFake(t, policy)
		fake.daemonID = testContainerDaemonID2

		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("a reservation bound to another daemon must block")
		}
		if !strings.Contains(err.Error(), "daemon") {
			t.Fatalf("error = %v, want an actionable daemon mismatch refusal", err)
		}
		for _, want := range []string{prior.DaemonID, filepath.Join(stateDir, containerSlotReservationFileName)} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want %q named", err, want)
			}
		}
		if strings.Contains(err.Error(), "DOCKER_HOST") {
			t.Fatalf("error = %v must not claim a recorded DOCKER_HOST or context", err)
		}
		if fake.callCount("inspect") != 0 {
			t.Fatalf("a daemon mismatch must not inspect the prior container: %v", fake.calls)
		}
		if fake.createCount() != 0 {
			t.Fatal("a daemon mismatch must block before create")
		}
		if removed := fake.removedIDs(); len(removed) != 0 {
			t.Fatalf("a daemon mismatch must not remove anything: %v", removed)
		}
		slot, openErr := OpenContainerSlot(stateDir)
		if openErr != nil {
			t.Fatalf("OpenContainerSlot: %v", openErr)
		}
		if _, ok := slot.Reservation(); !ok {
			t.Fatal("a daemon mismatch must retain the reservation")
		}
		_ = slot.Close()
	})

	t.Run("a legacy reservation with no daemon identity blocks", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		seedLegacyTestContainerRuntimeReservation(t, stateDir, prior)
		fake := newReadyFake(t, policy)

		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("a legacy reservation without a daemon identity must block")
		}
		if !strings.Contains(err.Error(), "daemon identity") {
			t.Fatalf("error = %v, want an actionable legacy-daemon refusal", err)
		}
		if fake.callCount("inspect") != 0 {
			t.Fatalf("a legacy reservation must not be inspected: %v", fake.calls)
		}
		if fake.createCount() != 0 {
			t.Fatal("a legacy reservation must block before create")
		}
		if removed := fake.removedIDs(); len(removed) != 0 {
			t.Fatalf("a legacy reservation must not be removed: %v", removed)
		}
		slot, openErr := OpenContainerSlot(stateDir)
		if openErr != nil {
			t.Fatalf("OpenContainerSlot: %v", openErr)
		}
		if _, ok := slot.Reservation(); !ok {
			t.Fatal("a legacy reservation must be retained for an explicit operator resolution")
		}
		_ = slot.Close()
	})

	t.Run("an unreadable daemon identity blocks before any reconciliation", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		prior := testContainerReservation()
		prior.ContainerID = testContainerID2
		seedTestContainerRuntimeReservation(t, stateDir, prior)
		fake := newReadyFake(t, policy)
		fake.daemonErr = errors.New("docker info transport failed")

		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("an unreadable daemon identity must block")
		}
		if !strings.Contains(err.Error(), "Docker Engine") {
			t.Fatalf("error = %v, want the failed daemon identity named", err)
		}
		if fake.callCount("inspect") != 0 {
			t.Fatalf("an unreadable daemon must not reconcile: %v", fake.calls)
		}
		if fake.createCount() != 0 {
			t.Fatal("an unreadable daemon must block before create")
		}
		slot, openErr := OpenContainerSlot(stateDir)
		if openErr != nil {
			t.Fatalf("OpenContainerSlot: %v", openErr)
		}
		if _, ok := slot.Reservation(); !ok {
			t.Fatal("an unreadable daemon must retain the reservation")
		}
		_ = slot.Close()
	})

	t.Run("an empty daemon identity blocks before any reconciliation", func(t *testing.T) {
		t.Parallel()
		stateDir := testContainerSlotStateDir(t)
		fake := newReadyFake(t, policy)
		fake.daemonID = ""

		runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
		if err == nil {
			_ = runtime.Close()
			t.Fatal("an empty daemon identity must block")
		}
		if fake.createCount() != 0 {
			t.Fatal("an empty daemon identity must block before create")
		}
		if removed := fake.removedIDs(); len(removed) != 0 {
			t.Fatalf("an empty daemon identity must not remove anything: %v", removed)
		}
		slot, openErr := OpenContainerSlot(stateDir)
		if openErr != nil {
			t.Fatalf("OpenContainerSlot: %v", openErr)
		}
		if _, ok := slot.Reservation(); ok {
			t.Fatal("a refusal before Reserve must not leave a reservation")
		}
		_ = slot.Close()
	})
}

func TestContainerRuntimeCleansUpEveryOpenFailureAfterReservation(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	cases := map[string]func(*fakeContainerDocker, *testing.T){
		"attach failure": func(fake *fakeContainerDocker, _ *testing.T) {
			fake.attachErr = errors.New("attach failed")
		},
		"cgroup mismatch": func(fake *fakeContainerDocker, _ *testing.T) {
			fake.execOutput = []byte("1\n0\n200000 100000\n128\n")
		},
		"host bounds mismatch": func(fake *fakeContainerDocker, t *testing.T) {
			wrong := policy
			wrong.CPUCount = 3
			fake.onCreate = func(args []string, containerID string) {
				name, runID, nonce, owner := testContainerCreateIdentity(t, args)
				fake.setDocument(containerID, testContainerRuntimeDocument(t, containerID, name, runID, nonce, owner, wrong, true))
			}
		},
	}
	for name, configure := range cases {
		name, configure := name, configure
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stateDir := testContainerSlotStateDir(t)
			fake := newReadyFake(t, policy)
			configure(fake, t)

			runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
			if err == nil {
				_ = runtime.Close()
				t.Fatalf("OpenContainerRuntime must fail for %s", name)
			}
			if runtime != nil {
				t.Fatal("a failed open must not return a runtime")
			}
			if got := fake.removedIDs(); !reflect.DeepEqual(got, []string{testContainerID}) {
				t.Fatalf("open failure cleanup removed %v, want the exact created container %s", got, testContainerID)
			}
			slot, openErr := OpenContainerSlot(stateDir)
			if openErr != nil {
				t.Fatalf("OpenContainerSlot after cleanup: %v", openErr)
			}
			if reservation, ok := slot.Reservation(); ok {
				t.Fatalf("cleanup must clear the reservation after proving absence, still holds %#v", reservation)
			}
			_ = slot.Close()
		})
	}
}

func TestContainerRuntimeCleansUpALostCreateOutcomeByReservedName(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	stateDir := testContainerSlotStateDir(t)
	fake := newReadyFake(t, policy)
	fake.createErr = errors.New("docker create failed after the container was created")
	// The controller never received the ID, so the only durable identity left
	// is the reserved name and its labels.
	fake.onCreate = func(args []string, containerID string) {
		name, runID, nonce, owner := testContainerCreateIdentity(t, args)
		fake.setDocument(name, testContainerRuntimeDocument(t, containerID, name, runID, nonce, owner, policy, false))
	}

	runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
	if err == nil {
		_ = runtime.Close()
		t.Fatal("OpenContainerRuntime must report the create failure")
	}
	if runtime != nil {
		t.Fatal("a failed open must not return a runtime")
	}
	if got := fake.removedIDs(); !reflect.DeepEqual(got, []string{testContainerID}) {
		t.Fatalf("lost-outcome cleanup removed %v, want the container found under the reserved name", got)
	}
	slot, openErr := OpenContainerSlot(stateDir)
	if openErr != nil {
		t.Fatalf("OpenContainerSlot after lost-outcome cleanup: %v", openErr)
	}
	if reservation, ok := slot.Reservation(); ok {
		t.Fatalf("lost-outcome cleanup must clear the reservation after proving absence, still holds %#v", reservation)
	}
	_ = slot.Close()
}

func TestContainerRuntimeFailedCleanupRetainsTheDurableBlock(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	stateDir := testContainerSlotStateDir(t)
	fake := newReadyFake(t, policy)
	fake.attachErr = errors.New("attach failed")
	fake.removeErr = errors.New("docker daemon busy")

	runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
	if err == nil {
		_ = runtime.Close()
		t.Fatal("OpenContainerRuntime must report the failed cleanup")
	}
	if runtime != nil {
		t.Fatal("a failed open must not return a runtime")
	}
	if !strings.Contains(err.Error(), "clean up") {
		t.Fatalf("error = %v, want the cleanup failure joined into the open error", err)
	}
	slot, openErr := OpenContainerSlot(stateDir)
	if openErr != nil {
		t.Fatalf("OpenContainerSlot after failed cleanup: %v", openErr)
	}
	reservation, ok := slot.Reservation()
	if !ok {
		t.Fatal("a failed cleanup must retain the durable reservation")
	}
	if reservation.ContainerID != testContainerID {
		t.Fatalf("retained reservation container ID = %q, want %q", reservation.ContainerID, testContainerID)
	}
	_ = slot.Close()

	// The retained block must stop the next attempt before it probes an image
	// or creates a container.
	next := newFakeContainerDocker()
	next.inspectErrors[testContainerID] = errors.New("docker transport still uncertain")
	nextRuntime, nextErr := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, next))
	if nextErr == nil {
		_ = nextRuntime.Close()
		t.Fatal("the next attempt must be blocked by the retained reservation")
	}
	if next.createCount() != 0 {
		t.Fatal("the blocked next attempt must not reach create")
	}
	if removed := next.removedIDs(); len(removed) != 0 {
		t.Fatalf("the blocked next attempt must not remove anything: %v", removed)
	}
}

func TestContainerRuntimeRefusesInvalidLifetimes(t *testing.T) {
	t.Parallel()
	cases := map[string]time.Duration{
		"zero":      0,
		"negative":  -time.Second,
		"above max": maxContainerLifetime + time.Second,
	}
	for name, lifetime := range cases {
		name, lifetime := name, lifetime
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			options := ContainerRuntimeOptions{
				Policy:   executionTestPolicy(),
				RunID:    testContainerRunID,
				Lifetime: lifetime,
			}
			runtime, err := OpenContainerRuntime(context.Background(), options)
			if err == nil {
				_ = runtime.Close()
				t.Fatalf("lifetime %s must be refused", lifetime)
			}
			if !strings.Contains(err.Error(), "lifetime") {
				t.Fatalf("error = %v, want a lifetime refusal", err)
			}
		})
	}
}

func TestContainerRuntimeCloseReleasesTheAttachPipeAndReapsTheClient(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	stateDir := testContainerSlotStateDir(t)
	fake := newReadyFake(t, policy)

	runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
	if err != nil {
		t.Fatalf("OpenContainerRuntime: %v", err)
	}
	process := fake.attachProcess()
	if process == nil {
		t.Fatal("the fake attach process was not retained")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- runtime.Close() }()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return; cleanup must be bounded")
	}
	select {
	case eof := <-process.eof:
		if !eof {
			t.Fatal("closing the controller writer must deliver EOF to the container stdin")
		}
	case <-time.After(time.Second):
		t.Fatal("the container stdin pipe did not reach EOF")
	}
	if !process.waited.Load() {
		t.Fatal("Close must reap the attach client")
	}
}

// gracefulEOFAttachProcess models an attach client whose inherited stdin sees
// EOF from the closed controller writer and which then finishes on its own. The
// finish gate keeps the client alive after EOF while the test inspects the
// attach context, so the ordering between a graceful EOF exit and the runtime's
// cancellation is observable instead of a scheduling race.
type gracefulEOFAttachProcess struct {
	ctx         context.Context
	eofObserved chan struct{}
	finish      chan struct{}
	finishOnce  sync.Once
	eofOK       atomic.Bool
	waited      atomic.Bool
}

func newGracefulEOFAttachProcess(ctx context.Context, readEnd *os.File) *gracefulEOFAttachProcess {
	process := &gracefulEOFAttachProcess{
		ctx:         ctx,
		eofObserved: make(chan struct{}),
		finish:      make(chan struct{}),
	}
	// The real child inherits the read end; the parent then closes its copy.
	// Duplicating here models that inheritance without racing the close.
	fd, err := syscall.Dup(int(readEnd.Fd()))
	if err != nil {
		close(process.eofObserved)
		return process
	}
	reader := os.NewFile(uintptr(fd), "test-graceful-attach-stdin")
	go func() {
		defer func() { _ = reader.Close() }()
		buffer := make([]byte, 1)
		_, readErr := reader.Read(buffer)
		process.eofOK.Store(errors.Is(readErr, io.EOF))
		close(process.eofObserved)
		<-process.finish
	}()
	return process
}

func (process *gracefulEOFAttachProcess) Wait() error {
	<-process.finish
	process.waited.Store(true)
	return nil
}

func (process *gracefulEOFAttachProcess) Kill() error {
	process.finishOnce.Do(func() { close(process.finish) })
	return nil
}

// TestContainerRuntimeCloseLetsAttachEOFCompleteBeforeCancellation pins the
// graceful shutdown ordering: the already-closed stdin pipe must be allowed to
// drive the attach client's normal completion, and the attach context must not
// be canceled while that healthy exit is still in progress. It also expects the
// attach context to be released once the client is reaped.
func TestContainerRuntimeCloseLetsAttachEOFCompleteBeforeCancellation(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	stateDir := testContainerSlotStateDir(t)
	fake := newReadyFake(t, policy)
	var process *gracefulEOFAttachProcess
	fake.attachFactory = func(ctx context.Context, readEnd *os.File) (containerAttachProcess, error) {
		process = newGracefulEOFAttachProcess(ctx, readEnd)
		return process, nil
	}

	runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
	if err != nil {
		t.Fatalf("OpenContainerRuntime: %v", err)
	}
	if process == nil {
		t.Fatal("the graceful attach process was not retained")
	}
	t.Cleanup(func() { _ = process.Kill() })

	closeResult := make(chan error, 1)
	go func() { closeResult <- runtime.Close() }()
	select {
	case <-process.eofObserved:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the controller writer never delivered EOF to the attach client")
	}
	if !process.eofOK.Load() {
		t.Fatal("the attach stdin read must end in EOF, not in an error")
	}
	if process.ctx.Err() != nil {
		t.Fatal("the attach context was canceled before the attach client could complete on its own")
	}
	process.finishOnce.Do(func() { close(process.finish) })
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the attach client finished")
	}
	if !process.waited.Load() {
		t.Fatal("Close must reap the attach client that reached EOF")
	}
	if process.ctx.Err() == nil {
		t.Fatal("Close must release the attach context after the client is reaped")
	}
}

// unreapedTestAttachProcess models an attach client that still cannot be reaped
// after Kill. Wait blocks until the test releases it, so the runtime's bounded
// kill and reap paths are the only thing that can end the wait.
type unreapedTestAttachProcess struct {
	done   chan struct{}
	killed atomic.Bool
	ctx    context.Context
}

func (process *unreapedTestAttachProcess) Wait() error {
	<-process.done
	return nil
}

func (process *unreapedTestAttachProcess) Kill() error {
	process.killed.Store(true)
	return nil
}

func TestContainerRuntimeUnreapedAttachRetainsTheDurableBlock(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	stateDir := testContainerSlotStateDir(t)
	fake := newReadyFake(t, policy)
	unreaped := &unreapedTestAttachProcess{done: make(chan struct{})}
	t.Cleanup(func() { close(unreaped.done) })
	fake.attachFactory = func(ctx context.Context, _ *os.File) (containerAttachProcess, error) {
		unreaped.ctx = ctx
		return unreaped, nil
	}

	runtime, err := OpenContainerRuntime(context.Background(), testContainerRuntimeOptions(stateDir, fake))
	if err != nil {
		t.Fatalf("OpenContainerRuntime: %v", err)
	}
	// The bounds are per-instance, so an unresponsive attach client fails fast
	// instead of making the test wait the production ten-second deadline.
	runtime.attachReapTimeout = 10 * time.Millisecond
	runtime.attachKillTimeout = 10 * time.Millisecond

	started := time.Now()
	if err := runtime.Close(); err == nil {
		t.Fatal("Close must report an attach client it could not reap")
	} else if !strings.Contains(err.Error(), "did not exit after being stopped") {
		t.Fatalf("Close error = %v, want the unreaped attach client refusal", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Close took %s; the tiny per-instance reap bounds must keep this failure bounded", elapsed)
	}
	if unreaped.ctx == nil || unreaped.ctx.Err() == nil {
		t.Fatal("Close must cancel the attach context of a client that ignored the closed pipe")
	}
	if !unreaped.killed.Load() {
		t.Fatal("Close must force-kill an attach client that ignored cancellation")
	}
	// The container is still removed and proven absent, but that is only the
	// absence half of cleanup: a failed earlier step keeps the durable block.
	if got := fake.removedIDs(); !reflect.DeepEqual(got, []string{testContainerID}) {
		t.Fatalf("cleanup removed %v, want the exact owned container %s", got, testContainerID)
	}
	slot, openErr := OpenContainerSlot(stateDir)
	if openErr != nil {
		t.Fatalf("OpenContainerSlot after failed cleanup: %v", openErr)
	}
	t.Cleanup(func() { _ = slot.Close() })
	reservation, ok := slot.Reservation()
	if !ok {
		t.Fatal("an unreaped attach client must retain the durable reservation even after its container is removed")
	}
	if reservation.ContainerID != testContainerID {
		t.Fatalf("retained reservation container ID = %q, want %q", reservation.ContainerID, testContainerID)
	}
}

func TestContainerRuntimeControllerHostSeamPermitsDarwinAndLinux(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	for _, hostOS := range []string{"linux", "darwin"} {
		hostOS := hostOS
		t.Run(hostOS, func(t *testing.T) {
			t.Parallel()
			stateDir := testContainerSlotStateDir(t)
			fake := newReadyFake(t, policy)
			options := testContainerRuntimeOptions(stateDir, fake)
			options.host.hostOS = func() string { return hostOS }

			runtime, err := OpenContainerRuntime(context.Background(), options)
			if err != nil {
				t.Fatalf("OpenContainerRuntime on a %s controller: %v", hostOS, err)
			}
			if err := runtime.Close(); err != nil {
				t.Fatalf("Close on a %s controller: %v", hostOS, err)
			}
		})
	}
}

func TestContainerRuntimeRefusesAnUnsupportedControllerHost(t *testing.T) {
	t.Parallel()
	stateDir := testContainerSlotStateDir(t)
	fake := newReadyFake(t, executionTestPolicy())
	options := testContainerRuntimeOptions(stateDir, fake)
	options.host.hostOS = func() string { return "windows" }

	runtime, err := OpenContainerRuntime(context.Background(), options)
	if err == nil {
		_ = runtime.Close()
		t.Fatal("an unsupported controller host must be refused")
	}
	if !strings.Contains(err.Error(), "windows") {
		t.Fatalf("error = %v, want the unsupported host named in the refusal", err)
	}
	if fake.createCount() != 0 {
		t.Fatal("an unsupported controller host must be refused before docker create")
	}
	if attached := fake.attachedIDs(); len(attached) != 0 {
		t.Fatalf("an unsupported controller host must not attach: %v", attached)
	}
}

func TestContainerRuntimeRequiresAPositiveUnprivilegedWorkerUID(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		uid int
		gid int
	}{
		"zero UID":     {uid: 0, gid: 20},
		"negative UID": {uid: -1, gid: 20},
		"negative GID": {uid: 501, gid: -1},
	}
	for name, current := range cases {
		name, current := name, current
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stateDir := testContainerSlotStateDir(t)
			fake := newReadyFake(t, executionTestPolicy())
			options := testContainerRuntimeOptions(stateDir, fake)
			options.host.currentUser = func() (int, int) { return current.uid, current.gid }

			runtime, err := OpenContainerRuntime(context.Background(), options)
			if err == nil {
				_ = runtime.Close()
				t.Fatalf("owner %d:%d must be refused as an unprivileged worker", current.uid, current.gid)
			}
			if !strings.Contains(err.Error(), "unprivileged worker") {
				t.Fatalf("error = %v, want the unprivileged worker refusal", err)
			}
			if fake.createCount() != 0 {
				t.Fatal("an invalid worker owner must be refused before docker create")
			}
		})
	}
}

func TestContainerRemoveMissingAcceptsOnlyDockersExactRmReply(t *testing.T) {
	t.Parallel()
	const identity = testContainerID2
	cases := map[string]struct {
		err      error
		identity string
		want     bool
	}{
		"exact missing-container reply": {
			err:      testContainerMissingAfterRemove(identity),
			identity: identity,
			want:     true,
		},
		"exact reply with a trailing newline": {
			err: &containerDockerError{
				Args:   []string{"rm", "--force", identity},
				Stderr: "Error response from daemon: No such container: " + identity + "\n",
				Err:    errors.New("exit status 1"),
			},
			identity: identity,
			want:     true,
		},
		"wrong identity": {
			err:      testContainerMissingAfterRemove(identity),
			identity: testContainerID,
			want:     false,
		},
		"empty identity": {
			err:      testContainerMissingAfterRemove(identity),
			identity: "",
			want:     false,
		},
		"broader text after the message": {
			err: &containerDockerError{
				Args:   []string{"rm", "--force", identity},
				Stderr: "Error response from daemon: No such container: " + identity + "\nwarning: retry",
				Err:    errors.New("exit status 1"),
			},
			identity: identity,
			want:     false,
		},
		"missing daemon prefix": {
			err: &containerDockerError{
				Args:   []string{"rm", "--force", identity},
				Stderr: "Error: No such container: " + identity,
				Err:    errors.New("exit status 1"),
			},
			identity: identity,
			want:     false,
		},
		"inspect-shaped reply": {
			err:      testContainerNoSuchObject(identity),
			identity: identity,
			want:     false,
		},
		"reply attributed to another verb": {
			err: &containerDockerError{
				Args:   []string{"inspect", identity},
				Stderr: "Error response from daemon: No such container: " + identity,
				Err:    errors.New("exit status 1"),
			},
			identity: identity,
			want:     false,
		},
		"reply with no recorded arguments": {
			err: &containerDockerError{
				Stderr: "Error response from daemon: No such container: " + identity,
				Err:    errors.New("exit status 1"),
			},
			identity: identity,
			want:     false,
		},
		"transport failure with no stderr": {
			err: &containerDockerError{
				Args: []string{"rm", "--force", identity},
				Err:  errors.New("connection reset by peer"),
			},
			identity: identity,
			want:     false,
		},
		"generic error": {
			err:      errors.New("docker daemon busy"),
			identity: identity,
			want:     false,
		},
		"nil error": {
			err:      nil,
			identity: identity,
			want:     false,
		},
	}
	for name, current := range cases {
		name, current := name, current
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := containerRemoveMissing(current.err, current.identity); got != current.want {
				t.Fatalf("containerRemoveMissing(%v, %q) = %t, want %t", current.err, current.identity, got, current.want)
			}
		})
	}
}

func TestContainerRuntimeRequiresRootPID1RegardlessOfImageUser(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	raw := testContainerRuntimeDocument(t, testContainerID, "wb-agent-"+testContainerRunID[len(IDPrefix):len(IDPrefix)+12]+"-0123456789abcdef", testContainerRunID, "0123456789abcdef0123456789abcdef", testContainerRuntimeOwner, policy, true)
	var documents []containerInspectDocument
	if err := json.Unmarshal(raw, &documents); err != nil {
		t.Fatal(err)
	}
	document := documents[0]
	document.Config.User = containerPID1User
	if err := verifyContainerPID1User(document); err != nil {
		t.Fatalf("root PID1 must verify: %v", err)
	}
	document.Config.User = "501:20"
	if err := verifyContainerPID1User(document); err == nil {
		t.Fatal("an image USER must not be allowed to run the PID1 watchdog")
	}
	document.Config.User = ""
	if err := verifyContainerPID1User(document); err == nil {
		t.Fatal("an empty Config.User must not stand in for the forced root PID1")
	}
}
