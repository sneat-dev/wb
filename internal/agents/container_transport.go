package agents

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/process"
)

// This file owns the transport into one verified container: the bounded
// environment the Docker client itself needs, the fixed Linux worker
// environment, the credential-only provider injection, and the unprivileged
// preparation of the private worker home.
//
// The Docker client environment is an explicit allowlist captured once when the
// runtime opens. It is used for metadata, for the harness command and for later
// validation alike, and it is never copied into the worker's environment. The
// worker environment is a separate fixed list that names only container paths.

const (
	// containerWorkerHomeDir is the private HOME the unprivileged worker owns.
	// It is created inside the container, never mounted from the host.
	containerWorkerHomeDir = "/tmp/wb-agent-home"
	// containerWorkerTmpDir is the only tmp location handed to the worker.
	containerWorkerTmpDir = "/tmp/wb-tmp"
	// containerWorkerToolWrapperDir is the fixed p1 tool-wrapper location.
	containerWorkerToolWrapperDir = "/tmp/wb-bin"
	// containerWorkerPath is the static Linux PATH. It never contains a host
	// directory and never depends on how the controller exported PATH.
	containerWorkerPath = containerWorkerToolWrapperDir + ":/opt/agent/node_modules/.bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	// containerProfileTemplate is the keyless public profile template shipped
	// in the image. Its node_modules is a symlink to the image tool tree.
	containerProfileTemplate = "/opt/wb-dsh-profile"
	// containerProfileNodeModules is the image tool tree the template links.
	containerProfileNodeModules = "/opt/agent/node_modules"
	// containerPrepareTimeout bounds the unprivileged preparation exec.
	containerPrepareTimeout = 2 * time.Minute
	// maxContainerCredentialValueBytes bounds one provider credential that is
	// placed in the Docker client environment.
	maxContainerCredentialValueBytes = 64 << 10
)

// containerTransportEnvironmentNames is the complete allowlist of host
// variables the Docker client may see. HOME and PATH help the client find its
// own configuration and helpers; the DOCKER_* names carry the selected
// context/host and TLS material. No provider credential is ever taken from the
// host here.
var containerTransportEnvironmentNames = []string{
	"HOME",
	"PATH",
	"DOCKER_CONFIG",
	"DOCKER_CONTEXT",
	"DOCKER_HOST",
	"DOCKER_TLS_VERIFY",
	"DOCKER_CERT_PATH",
	"DOCKER_API_VERSION",
}

// containerWorkerEnvironment is the fixed Linux environment every worker exec
// receives. Values are non-secret constants; a credential travels separately.
func containerWorkerEnvironment() []string {
	return []string{
		"PATH=" + containerWorkerPath,
		"HOME=" + containerWorkerHomeDir,
		"TMPDIR=" + containerWorkerTmpDir,
		"TMP=" + containerWorkerTmpDir,
		"TEMP=" + containerWorkerTmpDir,
		"XDG_CONFIG_HOME=/tmp/wb-config",
		"XDG_CACHE_HOME=/tmp/wb-cache",
		"XDG_STATE_HOME=/tmp/wb-state",
		"XDG_DATA_HOME=/tmp/wb-data",
		"GOCACHE=/tmp/wb-gocache",
		"GOMODCACHE=/tmp/wb-gomodcache",
		"GOPATH=/tmp/wb-gopath",
		"npm_config_cache=/tmp/wb-npm-cache",
		"PNPM_STORE_DIR=/tmp/wb-pnpm-store",
		"NX_DAEMON=false",
		"RAYON_NUM_THREADS=1",
		"NX_ISOLATE_PLUGINS=false",
	}
}

// containerTransportEnvironment builds the bounded Docker client environment.
// It always returns a non-nil slice so an empty result clears the child
// environment instead of accidentally inheriting the whole host environment.
func containerTransportEnvironment(lookup func(string) (string, bool)) []string {
	environment := make([]string, 0, len(containerTransportEnvironmentNames)+1)
	for _, name := range containerTransportEnvironmentNames {
		if lookup == nil {
			break
		}
		value, ok := lookup(name)
		if !ok || value == "" {
			continue
		}
		environment = append(environment, name+"="+value)
	}
	if !hasValue(environment, "PATH") {
		environment = append(environment, "PATH="+defaultPath)
	}
	return environment
}

// containerCredentialEnvironment copies the captured transport environment and
// adds exactly one resolved provider credential. The value is present only in
// the Docker client's environment; the argv carries the name alone.
func containerCredentialEnvironment(transport []string, credential Credential) []string {
	environment := make([]string, 0, len(transport)+1)
	environment = append(environment, transport...)
	if credential.EnvName == "" {
		return environment
	}
	environment = append(environment, credential.EnvName+"="+credential.Value)
	return environment
}

// reservedContainerWorkerEnvName reports whether a provider credential may not
// use this name because the transport, worker environment or container
// machinery already owns it.
func reservedContainerWorkerEnvName(name string) bool {
	for _, reserved := range containerTransportEnvironmentNames {
		if name == reserved {
			return true
		}
	}
	for _, entry := range containerWorkerEnvironment() {
		if strings.HasPrefix(entry, name+"=") {
			return true
		}
	}
	switch name {
	case "USER", "LOGNAME", "SHELL", "PWD", "OLDPWD", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
		"DOCKER_INSECURE_NO_VERIFY", "DOCKER_CLI_EXPERIMENTAL":
		return true
	}
	for _, prefix := range []string{
		"LD_", "DYLD_", "NODE_", "NX_", "DOCKER_", "SSL_CERT_", "XDG_",
		"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "ALL_PROXY",
		"https_proxy", "http_proxy", "no_proxy", "all_proxy",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// validContainerCredentialName accepts a conventional uppercase environment
// name. Lowercase and punctuation are refused so a credential name can never
// look like a Docker option or another environment mechanism.
func validContainerCredentialName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for index, character := range name {
		switch {
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
			if index == 0 {
				return false
			}
		case character == '_':
		default:
			return false
		}
	}
	return true
}

// validateContainerCredential checks the one provider credential this runtime
// accepts. An entirely empty credential is the credential-free validation case.
func validateContainerCredential(credential Credential) (string, error) {
	name := strings.TrimSpace(credential.EnvName)
	if name == "" {
		if credential.Value != "" {
			return "", errors.New("build a container worker command: a credential value requires a credential environment name")
		}
		return "", nil
	}
	if name != credential.EnvName {
		return "", errors.New("build a container worker command: the credential environment name has surrounding whitespace")
	}
	if !validContainerCredentialName(name) {
		return "", fmt.Errorf("build a container worker command: credential environment name %q is not an uppercase shell variable", name)
	}
	if reservedContainerWorkerEnvName(name) {
		return "", fmt.Errorf("build a container worker command: credential environment name %q is reserved for the container transport", name)
	}
	if credential.Value == "" {
		return "", fmt.Errorf("build a container worker command: credential %s has an empty value", name)
	}
	if len(credential.Value) > maxContainerCredentialValueBytes {
		return "", fmt.Errorf("build a container worker command: credential %s is %d bytes, above the %d-byte bound", name, len(credential.Value), maxContainerCredentialValueBytes)
	}
	if strings.ContainsRune(credential.Value, 0) {
		return "", fmt.Errorf("build a container worker command: credential %s contains a NUL byte", name)
	}
	return name, nil
}

// WorkerCommand builds the one Docker exec client a later owner uses for a
// worker command. The argv carries the exact verified container ID, the numeric
// owner and an explicit workdir, and passes the supplied argv without a shell.
// The credential contributes only its environment name to argv; its value
// travels in the Docker client environment and is never stored on the runtime
// or in a log.
func (runtime *ContainerRuntime) WorkerCommand(ctx context.Context, credential Credential, argv []string) (*exec.Cmd, error) {
	if runtime == nil {
		return nil, errors.New("build a container worker command: the runtime is nil")
	}
	credentialName, err := validateContainerCredential(credential)
	if err != nil {
		return nil, err
	}
	runtime.mutex.Lock()
	if runtime.closed {
		runtime.mutex.Unlock()
		return nil, errors.New("build a container worker command: the runtime is closed")
	}
	if runtime.containerID == "" {
		runtime.mutex.Unlock()
		return nil, errors.New("build a container worker command: the runtime has no verified container")
	}
	if runtime.workspace.worktreeDir == "" {
		runtime.mutex.Unlock()
		return nil, errors.New("build a container worker command: the runtime has no workspace worktree")
	}
	if len(argv) == 0 {
		runtime.mutex.Unlock()
		return nil, errors.New("build a container worker command: a non-empty worker argv is required")
	}
	for _, argument := range argv {
		if strings.ContainsRune(argument, 0) {
			runtime.mutex.Unlock()
			return nil, errors.New("build a container worker command: an argument contains a NUL byte")
		}
	}
	containerID := runtime.containerID
	ownerUser := runtime.ownerUser
	worktree := runtime.workspace.worktreeDir
	commandPath := runtime.dockerPath
	transport := append([]string(nil), runtime.transportEnv...)
	runtime.mutex.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	args := make([]string, 0, 16+len(containerWorkerEnvironment())*2+len(argv))
	args = append(args, "exec", "--user", ownerUser, "--workdir", worktree)
	for _, entry := range containerWorkerEnvironment() {
		args = append(args, "--env", entry)
	}
	if credentialName != "" {
		// Only the name enters argv. Docker forwards the value from the client
		// environment created below.
		args = append(args, "--env", credentialName)
	}
	args = append(args, containerID)
	args = append(args, argv...)
	command := process.CommandContext(ctx, commandPath, args...)
	command.Env = containerCredentialEnvironment(transport, Credential{EnvName: credentialName, Value: credential.Value})
	return command, nil
}

// ValidationTransport returns the credential-free command factory a contained
// validation run uses. The factory receives only the step argv, targets the same
// verified container and worktree as the harness, and keeps the bounded Docker
// client transport captured by this runtime. It cannot fall back to a host
// command: a runtime that cannot build the command returns the error instead.
func (runtime *ContainerRuntime) ValidationTransport() ValidationCommandFactory {
	return func(ctx context.Context, argv []string) (*exec.Cmd, error) {
		return runtime.WorkerCommand(ctx, Credential{}, argv)
	}
}

// PrepareWorkerHome creates the private worker HOME and every fixed /tmp cache
// directory inside the already verified container, as the unprivileged owner.
// The template is copied with a plain recursive copy, which does not attempt to
// preserve host ownership, and no host home, session or credential is copied.
func (runtime *ContainerRuntime) PrepareWorkerHome(ctx context.Context) error {
	if runtime == nil {
		return errors.New("prepare the container worker home: the runtime is nil")
	}
	runtime.mutex.Lock()
	if runtime.closed {
		runtime.mutex.Unlock()
		return errors.New("prepare the container worker home: the runtime is closed")
	}
	if runtime.containerID == "" {
		runtime.mutex.Unlock()
		return errors.New("prepare the container worker home: the runtime has no verified container")
	}
	if runtime.limits.MemoryBytes <= 0 {
		runtime.mutex.Unlock()
		return errors.New("prepare the container worker home: the container limits have not been verified")
	}
	containerID := runtime.containerID
	ownerUser := runtime.ownerUser
	command := runtime.commands.command
	runtime.mutex.Unlock()

	if command == nil {
		return errors.New("prepare the container worker home: the container command transport is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	prepareCtx, cancel := context.WithTimeout(ctx, containerPrepareTimeout)
	defer cancel()
	args := []string{"exec", "--user", ownerUser, "--workdir", "/tmp"}
	for _, entry := range []string{
		"HOME=" + containerWorkerHomeDir,
		"TMPDIR=" + containerWorkerTmpDir,
		"PATH=" + containerWorkerPath,
	} {
		args = append(args, "--env", entry)
	}
	args = append(args, containerID, "/bin/sh", "-c", containerPrepareScript())
	if _, err := command(prepareCtx, args); err != nil {
		return fmt.Errorf("prepare the container worker home in container %s: %w", containerID, err)
	}
	return nil
}

// containerPrepareScript is the fixed unprivileged preparation. It fails closed
// when the image lacks the profile template, its tool tree or the harness, and
// it never uses an ownership-preserving copy that would require root
// capabilities.
func containerPrepareScript() string {
	return fmt.Sprintf(containerPrepareScriptTemplate, containerWorkerHomeDir, containerWorkerTmpDir, containerWorkerToolWrapperDir, containerProfileTemplate, containerProfileNodeModules)
}

const containerPrepareScriptTemplate = `set -eu
umask 077
if [ -L %[1]s ]; then
  echo "the private worker home is a symlink" >&2
  exit 64
fi
test -d %[4]s
test -d %[5]s
command -v dsh >/dev/null 2>&1
mkdir -p %[1]s %[2]s %[3]s /tmp/wb-config /tmp/wb-cache /tmp/wb-state /tmp/wb-data /tmp/wb-gocache /tmp/wb-gomodcache /tmp/wb-gopath /tmp/wb-npm-cache /tmp/wb-pnpm-store
chmod 700 %[1]s
if [ ! -f %[1]s/.wb-prepared ]; then
  cp -R %[4]s/. %[1]s/
  : > %[1]s/.wb-prepared
fi
`
