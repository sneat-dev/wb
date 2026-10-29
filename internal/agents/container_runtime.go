package agents

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/process"
)

// This file owns the lifetime of one contained execution on this host. It is a
// controller, not a harness: the only process it starts on the host is Docker
// itself, and every workload runs inside the container.
//
// The durable authority for "one contained execution per user" is
// ContainerSlot, and the authority for "the image and the kernel limits are
// exactly the declared policy" is VerifyContainerImage and
// VerifyContainerCgroup. This file sequences them, keeps a held pipe as the
// owner-death signal, and removes only containers it can prove it owns.

const (
	// maxContainerLifetime bounds a runaway controller argument. The kernel
	// timeout inside the container is the backup deadline; an infinite one is
	// never expressible.
	maxContainerLifetime = 4 * time.Hour
	// containerPID1Script is the exact PID1 pattern proven worth using: the
	// supervising shell treats stdin EOF and the finite timeout as the same
	// termination signal, and Docker's init reaps the namespace.
	containerPID1Script = `trap "exit 143" TERM INT; exec 3<&0; (timeout -k 2s "$1" cat <&3 >/dev/null; kill -TERM $$) & exec 3<&-; wait`
	// containerMetadataTimeout bounds one Docker metadata invocation. It is
	// deliberately finite: an unresponsive daemon must not become an
	// unbounded wait.
	containerMetadataTimeout = 30 * time.Second
	// containerStartTimeout bounds the wait for a freshly started container to
	// report Running.
	containerStartTimeout = 15 * time.Second
	// containerStartPollInterval is the spacing between running-state inspects.
	containerStartPollInterval = 100 * time.Millisecond
	// containerCleanupTimeout bounds the independent cleanup context opened by
	// Close, so cleanup cannot inherit a canceled caller context.
	containerCleanupTimeout = 60 * time.Second
	// containerAttachReapTimeout bounds how long Close lets the already-closed
	// attach stdin drive the attach client's normal exit before the client is
	// canceled. A client that ignores both EOF and cancellation is force-killed
	// within containerAttachKillTimeout.
	containerAttachReapTimeout = 10 * time.Second
	// containerAttachKillTimeout bounds the wait for a canceled attach client and
	// again the wait after a forced Kill.
	containerAttachKillTimeout = 2 * time.Second
	// maxContainerCommandBytes bounds every Docker invocation before it can
	// allocate without limit. It is the same bound the inspect verifier uses.
	maxContainerCommandBytes = maxContainerInspectBytes
	// maxContainerDiagnosticBytes bounds the stderr kept for an error message.
	maxContainerDiagnosticBytes = 8 << 10
	// containerNamePrefix begins every generated container name. A name is
	// never accepted from a caller or from configuration.
	containerNamePrefix = "wb-agent-"
	// nanosPerCPU is the Docker NanoCpus representation of one CPU.
	nanosPerCPU int64 = 1_000_000_000
)

// containerDockerContextPattern bounds the context name Docker itself reports.
// A context name is used as an environment value only, but it still must not be
// empty, option-shaped or able to smuggle whitespace into the transport.
var containerDockerContextPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Container labels are the durable identity a recovery or cleanup must see
// before touching a container. They are fixed here and never supplied by a
// user or a profile.
const (
	containerLabelRunID = "dev.wb.agents.run-id"
	containerLabelNonce = "dev.wb.agents.nonce"
	containerLabelUser  = "dev.wb.agents.user"
)

// containerCgroupFiles are read in one exec. The order is what makes the four
// output lines unambiguous.
var containerCgroupFiles = []string{
	"/sys/fs/cgroup/memory.max",
	"/sys/fs/cgroup/memory.swap.max",
	"/sys/fs/cgroup/cpu.max",
	"/sys/fs/cgroup/pids.max",
}

// ContainerRuntimeOptions is the complete input to one contained execution.
// There is no field for extra Docker arguments, mounts, ports, environment or
// credentials: an unsupported need is refused later, not smuggled through here.
//
// Workspace is the one closed set of trusted host paths a later admission layer
// supplies. A nil Workspace keeps the lifecycle-only probe behaviour and plans
// no host mounts at all.
//
// The unexported fields are private dependency seams for focused tests. A
// production caller leaves them empty, which selects the fixed per-user state
// directory, the real docker binary and the current owner's UID/GID.
type ContainerRuntimeOptions struct {
	Policy    ExecutionPolicy
	RunID     string
	Lifetime  time.Duration
	Workspace *ContainerWorkspace

	stateDir string
	commands *containerCommands
	host     *containerHost
}

// containerDockerCommand runs one bounded Docker command. Tests replace it to
// record argv and return fixed documents; production uses the real binary.
type containerDockerCommand func(ctx context.Context, args []string) ([]byte, error)

// containerAttachProcess is the reaping surface of the attach client.
type containerAttachProcess interface {
	Wait() error
	Kill() error
}

// containerAttachCommand adapts the real *exec.Cmd to containerAttachProcess.
// Wait reaps the child exactly as Cmd.Wait does; Kill stops only the child
// process, never a process group or the Docker daemon.
type containerAttachCommand struct {
	command *exec.Cmd
}

func (attach *containerAttachCommand) Wait() error {
	return attach.command.Wait()
}

func (attach *containerAttachCommand) Kill() error {
	if attach == nil || attach.command == nil || attach.command.Process == nil {
		return errors.New("kill the docker attach client: no started process")
	}
	return attach.command.Process.Kill()
}

// containerDockerAttach starts the attach client with readEnd as its only
// inherited stdin. The returned process is waited on by the runtime.
type containerDockerAttach func(ctx context.Context, containerID string, readEnd *os.File) (containerAttachProcess, error)

// containerCommands is the narrow per-instance seam pair. Nothing else about
// Docker is injectable.
type containerCommands struct {
	command containerDockerCommand
	attach  containerDockerAttach
}

// containerHost is the narrow per-instance host seam: how the docker binary is
// found, which controller operating system this is, and which numeric owner the
// worker exec uses.
type containerHost struct {
	dockerPath  func() (string, error)
	hostOS      func() string
	currentUser func() (int, int)
	userHome    func() (string, error)
	lookupEnv   func(string) (string, bool)
}

func defaultContainerHost() *containerHost {
	return &containerHost{
		dockerPath:  func() (string, error) { return exec.LookPath("docker") },
		hostOS:      func() string { return goruntime.GOOS },
		currentUser: func() (int, int) { return os.Getuid(), os.Getgid() },
		userHome:    os.UserHomeDir,
		lookupEnv:   os.LookupEnv,
	}
}

// homeDirectory resolves the whole owner home that a workspace root may never
// be. A missing home is a refusal, not an invitation to skip the boundary.
func (host *containerHost) homeDirectory() (string, error) {
	if host != nil && host.userHome != nil {
		return host.userHome()
	}
	return os.UserHomeDir()
}

// lookupEnvironment reads one host variable for the Docker client transport.
func (host *containerHost) lookupEnvironment(name string) (string, bool) {
	if host != nil && host.lookupEnv != nil {
		return host.lookupEnv(name)
	}
	return os.LookupEnv(name)
}

// operatingSystem reports the controller platform. A focused test may pin the
// answer through the per-instance seam; production reads the real runtime.
func (host *containerHost) operatingSystem() string {
	if host.hostOS != nil {
		return host.hostOS()
	}
	return goruntime.GOOS
}

// containerControllerSupported reports whether this host may be the controller
// of a contained execution. Only the workload, image and kernel are required to
// be Linux; the WB metadata/controller itself also runs on macOS. Every other
// platform is refused.
func containerControllerSupported(hostOS string) bool {
	switch strings.ToLower(strings.TrimSpace(hostOS)) {
	case "linux", "darwin":
		return true
	default:
		return false
	}
}

func defaultContainerCommands(dockerPath string, transport []string) *containerCommands {
	// The captured transport is applied to every Docker child. A non-nil empty
	// slice is deliberately used: a nil Env would inherit the whole host.
	commandEnvironment := make([]string, len(transport))
	copy(commandEnvironment, transport)
	return &containerCommands{
		command: func(ctx context.Context, args []string) ([]byte, error) {
			return runBoundedDockerCommand(ctx, dockerPath, commandEnvironment, args)
		},
		attach: func(ctx context.Context, containerID string, readEnd *os.File) (containerAttachProcess, error) {
			// The proven attach client: interactive attach, controller-retained
			// write end. Only the read end is inherited, as stdin.
			command := process.CommandContext(ctx, dockerPath, "start", "--attach", "--interactive", containerID)
			command.Env = make([]string, len(commandEnvironment))
			copy(command.Env, commandEnvironment)
			command.Stdin = readEnd
			command.Stdout = io.Discard
			command.Stderr = io.Discard
			if err := command.Start(); err != nil {
				return nil, err
			}
			return &containerAttachCommand{command: command}, nil
		},
	}
}

// ContainerRuntime is one open, verified contained execution. It owns the
// durable reservation, the attach pipe and the exact container identity until
// Close proves the container absent and clears the reservation.
type ContainerRuntime struct {
	mutex sync.Mutex

	slot              *ContainerSlot
	policy            ExecutionPolicy
	runID             string
	daemonID          string
	containerLifetime time.Duration
	reservation       ContainerReservation
	reserved          bool
	ownerUser         string

	dockerPath string
	commands   *containerCommands
	// commandsInjected records that a focused test supplied the Docker seam.
	// Production leaves it false so the context pin can rebuild the command
	// closures with the pinned transport.
	commandsInjected bool
	host             *containerHost
	transportEnv     []string
	workspace        containerWorkspacePlan

	containerID string
	imageID     string
	limits      ContainerVerifiedLimits

	// writer is the only write end of the container's stdin pipe. Closing it
	// is the owner-death signal: Docker sees EOF and the PID1 supervisor takes
	// the namespace down with it.
	writer       *os.File
	attach       containerAttachProcess
	attachCancel context.CancelFunc
	attachDone   chan error
	// The reap bounds are per-instance so a focused test can prove the bounded
	// failure without a real multi-second wait. Production keeps the constants.
	attachReapTimeout time.Duration
	attachKillTimeout time.Duration

	closed   bool
	closeErr error
}

// OpenContainerRuntime admits exactly one contained execution. The durable
// global slot is taken before any Docker probe, a prior reservation is either
// recovered from a proven-stopped exact owned container or blocks, and the
// returned runtime is ready only after the image identity, the actual
// HostConfig bounds and the four cgroup files have been verified.
//
// Every error after the reservation is made runs the same independent cleanup:
// close the writer, remove only the exact owned container, prove absence, then
// clear the reservation. A cleanup failure is joined into the returned error
// and leaves the durable reservation in place, so the next attempt is blocked
// until someone proves absence.
func OpenContainerRuntime(ctx context.Context, options ContainerRuntimeOptions) (*ContainerRuntime, error) {
	runtime, err := newContainerRuntime(options)
	if err != nil {
		return nil, err
	}
	if err := runtime.open(ctx); err != nil {
		if cleanupErr := runtime.teardown(); cleanupErr != nil {
			return nil, errors.Join(err, fmt.Errorf("clean up after the refused container execution: %w", cleanupErr))
		}
		return nil, err
	}
	return runtime, nil
}

func newContainerRuntime(options ContainerRuntimeOptions) (*ContainerRuntime, error) {
	if err := validateExecutionPolicy(options.Policy); err != nil {
		return nil, err
	}
	if err := validateAgentID(options.RunID); err != nil {
		return nil, fmt.Errorf("container runtime run ID: %w", err)
	}
	if err := validateContainerLifetime(options.Lifetime); err != nil {
		return nil, err
	}
	host := options.host
	if host == nil {
		host = defaultContainerHost()
	}
	hostOS := host.operatingSystem()
	if !containerControllerSupported(hostOS) {
		return nil, fmt.Errorf("container execution requires a Linux or macOS controller host; this host runs %s", hostOS)
	}
	ownerUID, ownerGID := host.currentUser()
	if ownerUID <= 0 || ownerGID < 0 {
		return nil, errors.New("container execution requires a positive numeric owner UID and a non-negative numeric owner GID for the unprivileged worker")
	}
	workspace, err := normalizeContainerWorkspace(options.Workspace, host)
	if err != nil {
		return nil, err
	}
	transport := containerTransportEnvironment(host.lookupEnvironment)
	slot, err := OpenContainerSlot(options.stateDir)
	if err != nil {
		return nil, err
	}
	// No probes have run yet: the global slot is held before Docker is even
	// resolved. An error from here on is handled by Open's teardown, which
	// closes the slot but never clears a reservation it did not make.
	return &ContainerRuntime{
		slot:              slot,
		policy:            options.Policy,
		runID:             options.RunID,
		containerLifetime: options.Lifetime,
		ownerUser:         fmt.Sprintf("%d:%d", ownerUID, ownerGID),
		commands:          options.commands,
		commandsInjected:  options.commands != nil,
		host:              host,
		transportEnv:      transport,
		workspace:         workspace,
		attachReapTimeout: containerAttachReapTimeout,
		attachKillTimeout: containerAttachKillTimeout,
	}, nil
}

func (runtime *ContainerRuntime) open(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := runtime.probeDocker(); err != nil {
		return err
	}
	if runtime.commands == nil {
		runtime.commands = defaultContainerCommands(runtime.dockerPath, runtime.transportEnv)
	}
	// The resolved context is pinned before any command that reads or writes the
	// daemon, so a `docker context use` elsewhere cannot redirect a later exec,
	// reconcile or cleanup. An explicit DOCKER_HOST transport is already the
	// authority and is respected instead of being replaced by a context.
	injectedCommands := runtime.commands != nil && runtime.commandsInjected
	if err := runtime.pinDockerContext(ctx); err != nil {
		return err
	}
	if !injectedCommands {
		runtime.commands = defaultContainerCommands(runtime.dockerPath, runtime.transportEnv)
	}
	if err := runtime.captureDaemonIdentity(ctx); err != nil {
		return err
	}
	if err := runtime.recoverPriorReservation(ctx); err != nil {
		return err
	}
	imageID, err := runtime.inspectImage(ctx)
	if err != nil {
		return err
	}
	runtime.imageID = imageID
	reservation, err := runtime.newReservation()
	if err != nil {
		return err
	}
	// The reservation is durable before create, so a lost create outcome is
	// addressed by the reserved name and nonce rather than by scanning for
	// untracked containers.
	if err := runtime.slot.Reserve(reservation); err != nil {
		return err
	}
	runtime.reserved = true
	runtime.reservation = reservation
	if err := runtime.createContainer(ctx); err != nil {
		return err
	}
	if err := runtime.attachContainer(); err != nil {
		return err
	}
	if _, err := runtime.waitForContainerRunning(ctx); err != nil {
		return err
	}
	if err := runtime.attachExitedEarly(); err != nil {
		return err
	}
	if err := runtime.verifyContainerCgroups(ctx); err != nil {
		return err
	}
	return runtime.attachExitedEarly()
}

func validateContainerLifetime(lifetime time.Duration) error {
	if lifetime <= 0 {
		return fmt.Errorf("container lifetime must be positive and finite, got %s", lifetime)
	}
	if lifetime > maxContainerLifetime {
		return fmt.Errorf("container lifetime %s exceeds the supported maximum %s", lifetime, maxContainerLifetime)
	}
	return nil
}

func (runtime *ContainerRuntime) probeDocker() error {
	path, err := runtime.host.dockerPath()
	if err != nil {
		return fmt.Errorf("container execution requires the docker command-line tool: %w", err)
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("container execution requires the docker command-line tool; the resolver returned an empty path")
	}
	runtime.dockerPath = path
	return nil
}

// pinDockerContext resolves the current Docker context once and pins it into
// the captured transport. An explicit DOCKER_HOST or DOCKER_CONTEXT already
// pins the client and is left untouched, as are the TLS settings beside it.
// A resolution failure is a refusal: guessing a context could reach a daemon
// that does not own the durable reservation.
func (runtime *ContainerRuntime) pinDockerContext(ctx context.Context) error {
	if environmentHas(runtime.transportEnv, "DOCKER_HOST") || environmentHas(runtime.transportEnv, "DOCKER_CONTEXT") {
		return nil
	}
	raw, err := runtime.commands.command(ctx, []string{"context", "show"})
	if err != nil {
		return fmt.Errorf("resolve the current Docker context: %w; refusing to touch the container slot without a pinned daemon", err)
	}
	name := strings.TrimSpace(string(raw))
	if !containerDockerContextPattern.MatchString(name) {
		return fmt.Errorf("the resolved Docker context %q is not a bounded safe context name; refusing to touch the container slot without a pinned daemon", name)
	}
	runtime.transportEnv = append(runtime.transportEnv, "DOCKER_CONTEXT="+name)
	return nil
}

// captureDaemonIdentity reads and validates the actual Docker Engine ID from a
// bounded `docker info`. It is captured before reconciliation and before the
// durable reservation so the reservation can name the exact daemon that owns
// the container.
func (runtime *ContainerRuntime) captureDaemonIdentity(ctx context.Context) error {
	identity, err := runtime.readDaemonIdentity(ctx)
	if err != nil {
		return err
	}
	runtime.daemonID = identity
	return nil
}

func (runtime *ContainerRuntime) readDaemonIdentity(ctx context.Context) (string, error) {
	raw, err := runtime.commands.command(ctx, []string{"info", "--format", "{{.ID}}"})
	if err != nil {
		return "", fmt.Errorf("read the Docker Engine identity: %w; refusing to touch the container slot without a known daemon", err)
	}
	identity := strings.TrimSpace(string(raw))
	if !containerDaemonIDPattern.MatchString(identity) {
		return "", fmt.Errorf("docker info reported %q, not a bounded Docker Engine identity; refusing to touch the container slot", identity)
	}
	return identity, nil
}

// confirmDaemonIdentity re-reads the current Engine ID and requires it to match
// the daemon bound to the reservation. Every removal or stale-absence clear
// passes through here: absence on a different daemon proves nothing.
func (runtime *ContainerRuntime) confirmDaemonIdentity(ctx context.Context, reservation ContainerReservation) error {
	if reservation.DaemonID == "" {
		return fmt.Errorf("the container reservation for run %s has no Docker Engine identity; refusing to remove or clear it, because the container may belong to a different daemon", reservation.RunID)
	}
	identity, err := runtime.readDaemonIdentity(ctx)
	if err != nil {
		return err
	}
	if identity != reservation.DaemonID {
		return fmt.Errorf("the current Docker daemon (Engine ID %s) does not match the Docker Engine with recorded ID %s that owns the container reservation for run %s; refusing to remove or clear it. Restore or reach that Docker daemon and let the next contained run verify and recover the container; the durable reservation is %s and must never be deleted by hand", identity, reservation.DaemonID, reservation.RunID, runtime.slot.reservationPath())
	}
	return nil
}

func environmentHas(environment []string, name string) bool {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func (runtime *ContainerRuntime) inspectImage(ctx context.Context) (string, error) {
	raw, err := runtime.commands.command(ctx, []string{"image", "inspect", runtime.policy.Image})
	if err != nil {
		return "", fmt.Errorf("inspect the configured container image: %w", err)
	}
	return VerifyContainerImage(runtime.policy, raw)
}

func (runtime *ContainerRuntime) newReservation() (ContainerReservation, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return ContainerReservation{}, fmt.Errorf("generate the container reservation nonce: %w", err)
	}
	nonceHex := hex.EncodeToString(nonce)
	// validateAgentID already guarantees the agt- prefix and 32 hex
	// characters, so this slice is always in range.
	runSuffix := strings.TrimPrefix(runtime.runID, IDPrefix)
	reservation := ContainerReservation{
		RunID:    runtime.runID,
		Name:     containerNamePrefix + runSuffix[:12] + "-" + nonceHex[:16],
		Nonce:    nonceHex,
		DaemonID: runtime.daemonID,
	}
	if err := validateContainerReservation(reservation, false); err != nil {
		return ContainerReservation{}, err
	}
	return reservation, nil
}

func (runtime *ContainerRuntime) createContainer(ctx context.Context) error {
	args := runtime.containerCreateArgs(runtime.reservation)
	raw, err := runtime.commands.command(ctx, args)
	if err != nil {
		return fmt.Errorf("create the contained execution: %w", err)
	}
	containerID := strings.TrimSpace(string(raw))
	if !imageDigestHashPattern.MatchString(containerID) {
		return fmt.Errorf("docker create returned %q, not a full 64-character lowercase hex container ID", containerID)
	}
	runtime.containerID = containerID
	runtime.reservation.ContainerID = containerID
	if err := runtime.slot.RecordContainerID(containerID); err != nil {
		return fmt.Errorf("record the returned container ID: %w", err)
	}
	if updated, ok := runtime.slot.Reservation(); ok {
		runtime.reservation = updated
	}
	return nil
}

// containerCreateArgs is the whole accepted Docker surface. The only mounts it
// adds are the validated workspace bind/tmpfs plan; there are no ports, host
// namespaces, sockets, credentials or user arguments.
func (runtime *ContainerRuntime) containerCreateArgs(reservation ContainerReservation) []string {
	seconds := int64((runtime.lifetime() + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	args := []string{
		"create",
		"--name", reservation.Name,
		"--label", containerLabelRunID + "=" + reservation.RunID,
		"--label", containerLabelNonce + "=" + reservation.Nonce,
		"--label", containerLabelUser + "=" + runtime.ownerUser,
		"--init",
		// PID1 is the watchdog, not the worker. It must always run as root so
		// an image whose default USER is the unprivileged worker cannot let a
		// compromised worker signal its own supervisor. The worker itself is
		// always executed with the caller's positive numeric UID/GID.
		"--user", "0:0",
		"--interactive",
		"--attach", "stdin",
		"--attach", "stdout",
		"--attach", "stderr",
		"--memory", strconv.FormatInt(runtime.policy.MemoryBytes, 10),
		"--memory-swap", strconv.FormatInt(runtime.policy.MemorySwapBytes, 10),
		"--cpus", strconv.Itoa(runtime.policy.CPUCount),
		"--pids-limit", strconv.Itoa(runtime.policy.PIDsLimit),
		"--restart", "no",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--pull=never",
	}
	for _, mount := range runtime.workspace.mounts {
		args = append(args, mount.dockerArguments()...)
	}
	args = append(args,
		"--entrypoint", "/bin/sh",
		runtime.imageID,
		"-c", containerPID1Script,
		"wb-pid1",
		strconv.FormatInt(seconds, 10)+"s",
	)
	return args
}

// lifetime is the finite duration passed to the PID1 supervisor. It is stored
// separately from the policy so a later Close path never depends on a caller's
// mutable option value.
func (runtime *ContainerRuntime) lifetime() time.Duration {
	return runtime.containerLifetime
}

func (runtime *ContainerRuntime) attachContainer() error {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create the container attach stdin pipe: %w", err)
	}
	runtime.writer = writeEnd
	attachCtx, cancel := context.WithCancel(context.Background())
	attachProcess, attachErr := runtime.commands.attach(attachCtx, runtime.containerID, readEnd)
	closeErr := readEnd.Close()
	if attachErr != nil {
		cancel()
		return errors.Join(fmt.Errorf("attach to the contained execution: %w", attachErr), closeErr)
	}
	runtime.attach = attachProcess
	runtime.attachCancel = cancel
	runtime.attachDone = make(chan error, 1)
	go func() { runtime.attachDone <- attachProcess.Wait() }()
	if closeErr != nil {
		return fmt.Errorf("close the parent copy of the container attach stdin: %w", closeErr)
	}
	return nil
}

func (runtime *ContainerRuntime) waitForContainerRunning(ctx context.Context) (containerInspectDocument, error) {
	waitCtx, cancel := context.WithTimeout(ctx, containerStartTimeout)
	defer cancel()
	for {
		document, err := runtime.inspectContainer(waitCtx, runtime.containerID)
		if err != nil {
			return document, fmt.Errorf("inspect the new container %s: %w", runtime.containerID, err)
		}
		if err := runtime.verifyInspect(document); err != nil {
			return document, err
		}
		if document.State.Running {
			return document, nil
		}
		switch document.State.Status {
		case "exited", "dead":
			return document, fmt.Errorf("container %s exited before it became ready (status %q)", runtime.containerID, document.State.Status)
		}
		timer := time.NewTimer(containerStartPollInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return document, fmt.Errorf("container %s did not become running: %w", runtime.containerID, waitCtx.Err())
		case <-timer.C:
		}
	}
}

func (runtime *ContainerRuntime) verifyContainerCgroups(ctx context.Context) error {
	args := append([]string{"exec", runtime.containerID, "cat"}, containerCgroupFiles...)
	raw, err := runtime.commands.command(ctx, args)
	if err != nil {
		return fmt.Errorf("read the container cgroup limits: %w", err)
	}
	values, err := parseContainerCgroupValues(raw)
	if err != nil {
		return err
	}
	limits, err := VerifyContainerCgroup(runtime.policy, values)
	if err != nil {
		return fmt.Errorf("verify the container cgroup limits: %w", err)
	}
	runtime.limits = limits
	return nil
}

func parseContainerCgroupValues(raw []byte) (ContainerCgroupValues, error) {
	text := strings.TrimRight(string(raw), "\n")
	if strings.TrimSpace(text) == "" {
		return ContainerCgroupValues{}, errors.New("docker exec reported no cgroup values")
	}
	lines := strings.Split(text, "\n")
	if len(lines) != 4 {
		return ContainerCgroupValues{}, fmt.Errorf("docker exec reported %d cgroup lines; expected the four policy files", len(lines))
	}
	return ContainerCgroupValues{
		MemoryMax:     lines[0],
		MemorySwapMax: lines[1],
		CPUMax:        lines[2],
		PIDsMax:       lines[3],
	}, nil
}

// containerInspectDocument is the bounded subset of `docker inspect` this
// controller trusts. Unknown fields are ignored on purpose.
type containerInspectDocument struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		User   string            `json:"User"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		Memory      int64                      `json:"Memory"`
		MemorySwap  int64                      `json:"MemorySwap"`
		NanoCpus    int64                      `json:"NanoCpus"`
		PidsLimit   *int64                     `json:"PidsLimit"`
		Privileged  bool                       `json:"Privileged"`
		AutoRemove  bool                       `json:"AutoRemove"`
		Binds       []string                   `json:"Binds"`
		Volumes     map[string]json.RawMessage `json:"Volumes"`
		Tmpfs       map[string]string          `json:"Tmpfs"`
		CapAdd      []string                   `json:"CapAdd"`
		CapDrop     []string                   `json:"CapDrop"`
		SecurityOpt []string                   `json:"SecurityOpt"`
		NetworkMode string                     `json:"NetworkMode"`
		PidMode     string                     `json:"PidMode"`
		IpcMode     string                     `json:"IpcMode"`
		UsernsMode  string                     `json:"UsernsMode"`
		Restart     struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
		PortBindings map[string]json.RawMessage `json:"PortBindings"`
	} `json:"HostConfig"`
	State struct {
		Running bool   `json:"Running"`
		Pid     int    `json:"Pid"`
		Status  string `json:"Status"`
	} `json:"State"`
	Mounts []containerInspectMount `json:"Mounts"`
}

// containerInspectMount is Docker's actual Mounts array element shape. The
// verifier compares every element, so a source, destination, kind or read-write
// flag that was not planned is visible rather than inferred from create argv.
type containerInspectMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

func (runtime *ContainerRuntime) inspectContainer(ctx context.Context, identity string) (containerInspectDocument, error) {
	raw, err := runtime.commands.command(ctx, []string{"inspect", identity})
	if err != nil {
		return containerInspectDocument{}, err
	}
	if len(raw) > maxContainerInspectBytes {
		return containerInspectDocument{}, fmt.Errorf("docker inspect %s output is %d bytes, above the %d-byte bound", identity, len(raw), maxContainerInspectBytes)
	}
	var documents []containerInspectDocument
	if err := json.Unmarshal(raw, &documents); err != nil {
		return containerInspectDocument{}, fmt.Errorf("docker inspect %s output is not valid JSON: %w", identity, err)
	}
	if len(documents) != 1 {
		return containerInspectDocument{}, fmt.Errorf("docker inspect %s reported %d objects; exactly one container was expected", identity, len(documents))
	}
	return documents[0], nil
}

func (runtime *ContainerRuntime) verifyInspect(document containerInspectDocument) error {
	if err := verifyContainerOwnership(document, runtime.reservation, runtime.ownerUser); err != nil {
		return err
	}
	if err := verifyContainerPID1User(document); err != nil {
		return err
	}
	return verifyContainerHostConfig(runtime.policy, document, runtime.workspace.mounts)
}

// containerPID1User is the only accepted container PID1 identity. The image's
// own USER must never decide whether the worker can signal its watchdog.
const containerPID1User = "0:0"

// verifyContainerPID1User refuses a container whose PID1 would run as anyone
// but root. Docker reports the create --user value here, so an image's own USER
// can never substitute for the forced watchdog identity.
func verifyContainerPID1User(document containerInspectDocument) error {
	if user := strings.TrimSpace(document.Config.User); user != containerPID1User {
		return fmt.Errorf("container %s runs PID1 as %q; the watchdog must run as %q so the unprivileged worker cannot signal it", document.ID, document.Config.User, containerPID1User)
	}
	return nil
}

// verifyContainerOwnership checks only durable identity: full ID, exact name,
// and the fixed run/nonce/user labels. It never trusts a random lookalike.
func verifyContainerOwnership(document containerInspectDocument, reservation ContainerReservation, ownerUser string) error {
	if !imageDigestHashPattern.MatchString(document.ID) {
		return fmt.Errorf("container ID %q is not a full 64-character lowercase hex ID", document.ID)
	}
	if reservation.ContainerID != "" && document.ID != reservation.ContainerID {
		return fmt.Errorf("container %s does not match the reserved container ID %s", document.ID, reservation.ContainerID)
	}
	name := strings.TrimPrefix(document.Name, "/")
	if name != reservation.Name {
		return fmt.Errorf("container %s has name %q, not the reserved name %q", document.ID, name, reservation.Name)
	}
	labels := document.Config.Labels
	if got := labels[containerLabelRunID]; got != reservation.RunID {
		return fmt.Errorf("container %s has run label %q, not the reserved %q", document.ID, got, reservation.RunID)
	}
	if got := labels[containerLabelNonce]; got != reservation.Nonce {
		return fmt.Errorf("container %s has nonce label %q, not the reserved %q", document.ID, got, reservation.Nonce)
	}
	if got := labels[containerLabelUser]; got != ownerUser {
		return fmt.Errorf("container %s has user label %q, not the current owner %q", document.ID, got, ownerUser)
	}
	return nil
}

// verifyContainerHostConfig checks the bounds Docker actually applied, not the
// arguments we hoped it applied. It covers the memory, CPU and PIDs bounds, the
// security and namespace surface the controller sets, the closed mount
// allowlist and the capability/privilege posture this policy forbids. It is a
// defense-in-depth check of the fields it models, not a complete audit of every
// possible Docker resource.
func verifyContainerHostConfig(policy ExecutionPolicy, document containerInspectDocument, expectedMounts []containerMount) error {
	host := document.HostConfig
	if host.Memory != policy.MemoryBytes {
		return fmt.Errorf("container %s HostConfig memory %d does not match agents.execution memory_bytes %d", document.ID, host.Memory, policy.MemoryBytes)
	}
	if host.MemorySwap != policy.MemorySwapBytes {
		return fmt.Errorf("container %s HostConfig memory_swap %d does not match agents.execution memory_swap_bytes %d", document.ID, host.MemorySwap, policy.MemorySwapBytes)
	}
	if host.NanoCpus%nanosPerCPU != 0 || host.NanoCpus/nanosPerCPU != int64(policy.CPUCount) {
		return fmt.Errorf("container %s HostConfig NanoCpus %d does not express exactly %d CPUs", document.ID, host.NanoCpus, policy.CPUCount)
	}
	if host.PidsLimit == nil {
		return fmt.Errorf("container %s HostConfig reports no pids limit; agents.execution pids_limit %d is mandatory", document.ID, policy.PIDsLimit)
	}
	if *host.PidsLimit != int64(policy.PIDsLimit) {
		return fmt.Errorf("container %s HostConfig pids limit %d does not match agents.execution pids_limit %d", document.ID, *host.PidsLimit, policy.PIDsLimit)
	}
	if host.Privileged {
		return fmt.Errorf("container %s is privileged; the mandatory policy does not permit it", document.ID)
	}
	if host.AutoRemove {
		return fmt.Errorf("container %s is auto-removed; the controller must prove absence itself", document.ID)
	}
	if host.Restart.Name != "no" {
		return fmt.Errorf("container %s restart policy is %q; the mandatory policy requires no", document.ID, host.Restart.Name)
	}
	if err := verifyContainerMounts(document, expectedMounts); err != nil {
		return err
	}
	if host.NetworkMode != "" && host.NetworkMode != "default" && host.NetworkMode != "bridge" && host.NetworkMode != "none" {
		return fmt.Errorf("container %s uses network mode %q; no host or shared network namespace is permitted", document.ID, host.NetworkMode)
	}
	if host.PidMode != "" && host.PidMode != "private" {
		return fmt.Errorf("container %s uses PID mode %q; no host or shared PID namespace is permitted", document.ID, host.PidMode)
	}
	if host.IpcMode != "" && host.IpcMode != "private" {
		return fmt.Errorf("container %s uses IPC mode %q; the private IPC namespace is required", document.ID, host.IpcMode)
	}
	if host.UsernsMode != "" && host.UsernsMode != "private" {
		return fmt.Errorf("container %s uses user namespace mode %q; the controller's namespace mode must not be overridden", document.ID, host.UsernsMode)
	}
	if len(host.PortBindings) != 0 {
		return fmt.Errorf("container %s publishes ports; no port mapping is permitted", document.ID)
	}
	if !containsFold(host.CapDrop, "ALL") {
		return fmt.Errorf("container %s does not drop all capabilities", document.ID)
	}
	// CapDrop ALL is only a drop list: a simultaneous --cap-add would still
	// re-grant capabilities, so a non-empty addition is refused even though the
	// controller never emits one.
	if len(host.CapAdd) != 0 {
		return fmt.Errorf("container %s adds capabilities %v; cap-drop ALL is mandatory with no additions", document.ID, host.CapAdd)
	}
	if !hasNoNewPrivileges(host.SecurityOpt) {
		return fmt.Errorf("container %s does not set no-new-privileges", document.ID)
	}
	return nil
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func hasNoNewPrivileges(options []string) bool {
	for _, option := range options {
		if option == "no-new-privileges" || strings.EqualFold(option, "no-new-privileges:true") {
			return true
		}
	}
	return false
}

// recoverPriorReservation handles the durable reservation left by a previous
// holder. A live or unowned container is a refusal, never an automatic kill;
// only an exactly owned, stopped container is removed and proven absent under
// the retained slot lock.
func (runtime *ContainerRuntime) recoverPriorReservation(ctx context.Context) error {
	prior, ok := runtime.slot.Reservation()
	if !ok {
		return nil
	}
	// A reservation without a daemon binding (legacy) or one bound to another
	// Engine is never treated as stale: NoSuchObject from a different daemon is
	// not evidence that the owning daemon stopped the container.
	if prior.DaemonID == "" {
		return fmt.Errorf("a previous container reservation for run %s predates Docker Engine binding and has no daemon identity; refusing to inspect, remove or clear it, because the container may still be running on the daemon that owns it", prior.RunID)
	}
	if prior.DaemonID != runtime.daemonID {
		return fmt.Errorf("a previous container reservation for run %s is bound to the Docker Engine with recorded ID %s, but the current daemon is %s; refusing to inspect, remove or clear it, because absence on another daemon proves nothing. Restore or reach that Docker Engine and let the next contained run verify and recover the container; the durable reservation is %s and must never be deleted by hand", prior.RunID, prior.DaemonID, runtime.daemonID, runtime.slot.reservationPath())
	}
	identity := prior.ContainerID
	switch {
	case identity != "":
	case prior.Name != "":
		identity = prior.Name
	default:
		return fmt.Errorf("a previous container reservation for run %s carries no identity; refusing to guess which container it owns", prior.RunID)
	}
	document, err := runtime.inspectContainer(ctx, identity)
	if err != nil {
		if containerNoSuchObject(err, identity) {
			if clearErr := runtime.slot.ClearReservation(func(ContainerReservation) error {
				if err := runtime.confirmDaemonIdentity(ctx, prior); err != nil {
					return err
				}
				return runtime.proveAbsent(ctx, identity)
			}); clearErr != nil {
				return fmt.Errorf("clear the stale container reservation for run %s: %w", prior.RunID, clearErr)
			}
			return nil
		}
		return fmt.Errorf("a previous container reservation for run %s could not be verified (container %s); refusing to start another until it is resolved: %w", prior.RunID, identity, err)
	}
	if err := verifyContainerOwnership(document, prior, runtime.ownerUser); err != nil {
		return fmt.Errorf("a previous container reservation for run %s does not match the container it names; refusing to remove it: %w", prior.RunID, err)
	}
	if document.State.Running || document.State.Pid != 0 {
		return fmt.Errorf("a previous container execution for run %s is still running (container %s); wait for its lifetime to expire or stop it before starting another", prior.RunID, document.ID)
	}
	if err := runtime.confirmDaemonIdentity(ctx, prior); err != nil {
		return fmt.Errorf("confirm the daemon that owns the stopped prior container %s for run %s: %w", document.ID, prior.RunID, err)
	}
	if err := runtime.removeContainer(ctx, document.ID); err != nil {
		return fmt.Errorf("remove the stopped prior container %s for run %s: %w", document.ID, prior.RunID, err)
	}
	if err := runtime.proveAbsent(ctx, document.ID); err != nil {
		return fmt.Errorf("prove the stopped prior container %s absent: %w", document.ID, err)
	}
	if err := runtime.slot.ClearReservation(func(ContainerReservation) error {
		if err := runtime.confirmDaemonIdentity(ctx, prior); err != nil {
			return err
		}
		return runtime.proveAbsent(ctx, document.ID)
	}); err != nil {
		return fmt.Errorf("clear the recovered container reservation for run %s: %w", prior.RunID, err)
	}
	return nil
}

func (runtime *ContainerRuntime) removeContainer(ctx context.Context, containerID string) error {
	if _, err := runtime.commands.command(ctx, []string{"rm", "--force", containerID}); err != nil {
		if containerNoSuchObject(err, containerID) {
			return nil
		}
		return fmt.Errorf("docker rm --force %s: %w", containerID, err)
	}
	return nil
}

// proveAbsent accepts only Docker's exact no-such-object answer for the exact
// identity. A transport error, a generic failure or a still-present container
// is not absence.
func (runtime *ContainerRuntime) proveAbsent(ctx context.Context, identity string) error {
	if identity == "" {
		return errors.New("prove a container absent: the identity is empty")
	}
	_, err := runtime.inspectContainer(ctx, identity)
	if err == nil {
		return fmt.Errorf("container %s is still present", identity)
	}
	if containerNoSuchObject(err, identity) {
		return nil
	}
	return fmt.Errorf("container %s could not be proven absent: %w", identity, err)
}

// Close cleans up the exact owned container and releases the slot. It is
// idempotent; a repeated Close returns the first result rather than pretending
// a failed cleanup succeeded. Cleanup uses its own finite context, so a
// canceled caller cannot shorten the absence proof.
func (runtime *ContainerRuntime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	if runtime.closed {
		return runtime.closeErr
	}
	runtime.closed = true
	runtime.closeErr = runtime.teardownLocked()
	return runtime.closeErr
}

// teardown is the error-path cleanup used by Open. It does not mark the runtime
// closed (the runtime is never returned) but it performs exactly Close's work.
func (runtime *ContainerRuntime) teardown() error {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	return runtime.teardownLocked()
}

func (runtime *ContainerRuntime) teardownLocked() error {
	var errs []error
	if runtime.writer != nil {
		if err := runtime.writer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close the container attach stdin: %w", err))
		}
		runtime.writer = nil
	}
	if err := runtime.reapAttachLocked(); err != nil {
		errs = append(errs, err)
	}
	if runtime.reserved {
		// Removing and proving the container absent is always attempted, even
		// after an earlier cleanup step failed. Clearing the durable reservation
		// is a separate decision: it happens only when every earlier step
		// succeeded. An unreaped attach client or a failed writer close leaves
		// cleanup unverified, so the next holder must stay blocked.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
		proofIdentity, cleanupErr := runtime.removeOwnedContainerLocked(cleanupCtx)
		if cleanupErr != nil {
			errs = append(errs, cleanupErr)
		} else if len(errs) == 0 {
			if clearErr := runtime.clearProvenReservationLocked(cleanupCtx, proofIdentity); clearErr != nil {
				errs = append(errs, clearErr)
			}
		}
		cancel()
	}
	if runtime.slot != nil {
		if err := runtime.slot.Close(); err != nil {
			errs = append(errs, fmt.Errorf("release the container slot: %w", err))
		}
	}
	return errors.Join(errs...)
}

// reapAttachLocked ends the attach client. teardownLocked closes the controller
// write end before calling this, so the normal path is the already-closed pipe
// delivering EOF to the client, which then exits on its own. Canceling first
// would race that healthy EOF and force a kill instead of a graceful reap, so
// the context cancellation is held back for the whole bounded reap window. Only
// a client that ignores the closed pipe is canceled, and then killed, within the
// existing finite bounds. A client that still cannot be reaped is reported, and
// teardownLocked keeps the durable reservation because an unreaped attach client
// leaves cleanup unverified.
func (runtime *ContainerRuntime) reapAttachLocked() error {
	if runtime.attach == nil {
		if runtime.attachCancel != nil {
			runtime.attachCancel()
			runtime.attachCancel = nil
		}
		return nil
	}
	graceful := false
	if runtime.attachDone != nil {
		select {
		case <-runtime.attachDone:
			graceful = true
		case <-time.After(runtime.attachReapTimeout):
		}
	}
	if graceful {
		if runtime.attachCancel != nil {
			// Release the attach context even on a graceful exit: nothing else
			// owns it once the client has been reaped.
			runtime.attachCancel()
			runtime.attachCancel = nil
		}
		runtime.attachDone = nil
		runtime.attach = nil
		return nil
	}
	if runtime.attachCancel != nil {
		runtime.attachCancel()
		runtime.attachCancel = nil
	}
	// After the grace window, cancellation and the forced kill are issued
	// together and only one existing kill timeout is waited on. That keeps the
	// original combined reap-plus-kill bound instead of adding a second window.
	if runtime.attach != nil {
		_ = runtime.attach.Kill()
	}
	if runtime.attachDone != nil {
		select {
		case <-runtime.attachDone:
		case <-time.After(runtime.attachKillTimeout):
			runtime.attach = nil
			runtime.attachDone = nil
			return fmt.Errorf("the docker attach client for container %s did not exit after being stopped", runtime.containerID)
		}
		runtime.attachDone = nil
	}
	runtime.attach = nil
	return nil
}

func (runtime *ContainerRuntime) attachExitedEarly() error {
	if runtime.attachDone == nil {
		return nil
	}
	select {
	case err := <-runtime.attachDone:
		runtime.attachDone = nil
		if err != nil {
			return fmt.Errorf("the docker attach client for container %s exited before the container became ready: %w", runtime.containerID, err)
		}
		return fmt.Errorf("the docker attach client for container %s exited before the container became ready", runtime.containerID)
	default:
		return nil
	}
}

// removeOwnedContainerLocked is the absence half of cleanup. It inspects the
// reserved identity, removes only a container whose durable identity verifies,
// and proves that exact container absent. It returns the identity that was
// proven absent; it never touches the durable reservation itself.
func (runtime *ContainerRuntime) removeOwnedContainerLocked(ctx context.Context) (string, error) {
	reservation, ok := runtime.slot.Reservation()
	if !ok {
		reservation = runtime.reservation
	}
	identity := runtime.containerID
	if identity == "" {
		identity = reservation.ContainerID
	}
	if identity == "" {
		identity = reservation.Name
	}
	if identity == "" {
		return "", errors.New("clean up the contained execution: no reserved identity is available to prove absence")
	}
	// The daemon binding is checked against the live Engine before any removal.
	// A switched, missing or unreadable daemon retains the reservation because
	// the container may still be running on the original one.
	if err := runtime.confirmDaemonIdentity(ctx, reservation); err != nil {
		return "", err
	}
	provenIdentity := ""
	document, err := runtime.inspectContainer(ctx, identity)
	switch {
	case err == nil:
		if verifyErr := verifyContainerOwnership(document, reservation, runtime.ownerUser); verifyErr != nil {
			return "", fmt.Errorf("refusing to remove container %s during cleanup: %w", document.ID, verifyErr)
		}
		if removalErr := runtime.removeContainer(ctx, document.ID); removalErr != nil {
			return "", fmt.Errorf("remove the owned container %s during cleanup: %w", document.ID, removalErr)
		}
		provenIdentity = document.ID
	case containerNoSuchObject(err, identity):
		provenIdentity = identity
	default:
		return "", fmt.Errorf("verify the owned container %s before cleanup: %w", identity, err)
	}
	if err := runtime.proveAbsent(ctx, provenIdentity); err != nil {
		return "", err
	}
	return provenIdentity, nil
}

// clearProvenReservationLocked is the clear half of cleanup. The caller must
// already have proven the exact container absent and every earlier cleanup step
// must have succeeded; this function makes the same absence proof again under
// the retained slot lock before the durable reservation is removed.
func (runtime *ContainerRuntime) clearProvenReservationLocked(ctx context.Context, proofIdentity string) error {
	if err := runtime.slot.ClearReservation(func(prior ContainerReservation) error {
		// Presence/absence is meaningful only on the daemon bound to the
		// reservation, so the same check precedes the second proof.
		if err := runtime.confirmDaemonIdentity(ctx, prior); err != nil {
			return err
		}
		identity := prior.ContainerID
		if identity == "" {
			identity = proofIdentity
		}
		if identity == "" {
			identity = prior.Name
		}
		return runtime.proveAbsent(ctx, identity)
	}); err != nil {
		return fmt.Errorf("clear the container reservation after cleanup: %w", err)
	}
	runtime.reserved = false
	return nil
}

// ContainerID returns the full Docker ID of the verified container.
func (runtime *ContainerRuntime) ContainerID() string {
	if runtime == nil {
		return ""
	}
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	return runtime.containerID
}

// ImageID returns the immutable config image ID the container was created from.
func (runtime *ContainerRuntime) ImageID() string {
	if runtime == nil {
		return ""
	}
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	return runtime.imageID
}

// VerifiedLimits returns the bounds independently read from the container's
// cgroup files. It is only meaningful once Open has returned successfully.
func (runtime *ContainerRuntime) VerifiedLimits() ContainerVerifiedLimits {
	if runtime == nil {
		return ContainerVerifiedLimits{}
	}
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	return runtime.limits
}

// ExecArgv is the narrow builder a later owner uses to run one worker command.
// It fixes the exact container ID and the current owner's numeric UID/GID, and
// passes argv through without a shell. It adds no environment, mount or
// user-supplied Docker option.
func (runtime *ContainerRuntime) ExecArgv(argv []string) ([]string, error) {
	if runtime == nil {
		return nil, errors.New("build a container exec argv: the runtime is nil")
	}
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	if runtime.closed {
		return nil, errors.New("build a container exec argv: the runtime is closed")
	}
	if runtime.containerID == "" {
		return nil, errors.New("build a container exec argv: the runtime has no verified container")
	}
	if len(argv) == 0 {
		return nil, errors.New("build a container exec argv: a non-empty worker argv is required")
	}
	for _, argument := range argv {
		if strings.ContainsRune(argument, 0) {
			return nil, errors.New("build a container exec argv: an argument contains a NUL byte")
		}
	}
	args := make([]string, 0, 4+len(argv))
	args = append(args, "exec", "--user", runtime.ownerUser, runtime.containerID)
	args = append(args, argv...)
	return args, nil
}

// boundedContainerOutput bounds Docker output before it can grow without limit.
// The copy returning an error ends the read, so the bound is enforced during
// allocation rather than after a CombinedOutput has already happened.
type boundedContainerOutput struct {
	limit    int
	buffer   bytes.Buffer
	exceeded bool
}

var errContainerOutputExceeded = errors.New("container runtime output exceeded its bound")

func (output *boundedContainerOutput) Write(data []byte) (int, error) {
	if len(data) > output.limit-output.buffer.Len() {
		output.exceeded = true
		return 0, errContainerOutputExceeded
	}
	return output.buffer.Write(data)
}

// containerDockerError is one failed Docker invocation. Its stderr is bounded
// and is the only source the absence proof accepts.
type containerDockerError struct {
	Args   []string
	Stderr string
	Err    error
}

func (err *containerDockerError) Error() string {
	detail := strings.TrimSpace(err.Stderr)
	if detail != "" {
		return fmt.Sprintf("docker %s failed: %s", strings.Join(err.Args, " "), detail)
	}
	return fmt.Sprintf("docker %s failed: %v", strings.Join(err.Args, " "), err.Err)
}

func (err *containerDockerError) Unwrap() error {
	return err.Err
}

// containerNoSuchObject accepts only Docker's exact no-such-object answer for
// this exact identity. A generic failure or another object's message is not
// absence.
func containerNoSuchObject(err error, identity string) bool {
	var dockerErr *containerDockerError
	if !errors.As(err, &dockerErr) || identity == "" {
		return false
	}
	needle := "no such object: " + strings.ToLower(identity)
	for _, line := range strings.Split(strings.ToLower(dockerErr.Stderr), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), needle) {
			return true
		}
	}
	return false
}

func runBoundedDockerCommand(ctx context.Context, dockerPath string, environment []string, args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("run a docker command: no arguments")
	}
	commandCtx, cancel := context.WithTimeout(ctx, containerMetadataTimeout)
	defer cancel()
	command := process.CommandContext(commandCtx, dockerPath, args...)
	command.Env = make([]string, len(environment))
	copy(command.Env, environment)
	stdout := &boundedContainerOutput{limit: maxContainerCommandBytes}
	stderr := &boundedContainerOutput{limit: maxContainerDiagnosticBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if stdout.exceeded {
		return nil, fmt.Errorf("docker %s output is above the %d-byte bound", args[0], maxContainerCommandBytes)
	}
	if err != nil {
		detail := strings.TrimSpace(stderr.buffer.String())
		if stderr.exceeded {
			detail += " (diagnostics truncated)"
		}
		return nil, &containerDockerError{Args: append([]string(nil), args...), Stderr: detail, Err: err}
	}
	return stdout.buffer.Bytes(), nil
}
