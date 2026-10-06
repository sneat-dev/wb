package graduation

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/filewrite"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxEvidenceBytes = 4 << 20

var graduationRemoteName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// EvidencePaths names independently produced evidence and an optional exclusive output.
type EvidencePaths struct{ LocalCheck, CIWait, RemoteTarget, DeployedRevision, TerminalCleanup, Output string }

// RemoteTargetRequest binds an observation to one configured remote and exact branch.
type RemoteTargetRequest struct{ Repository, RepositoryPath, Remote, Target, Output string }

// Observer executes evidence composition and authoritative Git observation without CLI dependencies.
type Observer struct {
	Now         func() time.Time
	RunGit      func(context.Context, string, ...string) ([]byte, error)
	resolvePath func(string) (string, error)
}

func DefaultObserver() Observer {
	return Observer{Now: func() time.Time { return time.Now().UTC() }, RunGit: func(ctx context.Context, directory string, arguments ...string) ([]byte, error) {
		args := append([]string{"-C", directory}, arguments...)
		return exec.CommandContext(ctx, "git", args...).Output()
	}}
}
func (service Observer) ComposeFiles(paths EvidencePaths) ([]byte, error) {
	localCheckRaw, err := readEvidence(paths.LocalCheck, "--local-check")
	if err != nil {
		return nil, err
	}
	ciWaitRaw, err := readEvidence(paths.CIWait, "--ci-wait")
	if err != nil {
		return nil, err
	}
	remoteTargetRaw, err := readEvidence(paths.RemoteTarget, "--remote-target")
	if err != nil {
		return nil, err
	}
	deployedRaw, err := readEvidence(paths.DeployedRevision, "--deployed-revision")
	if err != nil {
		return nil, err
	}
	cleanupRaw, err := readEvidence(paths.TerminalCleanup, "--terminal-cleanup")
	if err != nil {
		return nil, err
	}

	localCheck, err := DecodeVerificationIndex(localCheckRaw)
	if err != nil {
		return nil, fmt.Errorf("decode --local-check: %w", err)
	}
	ciWait, err := DecodeCIWaitReceipt(ciWaitRaw)
	if err != nil {
		return nil, fmt.Errorf("decode --ci-wait: %w", err)
	}
	remoteTarget, err := DecodeRemoteTarget(remoteTargetRaw)
	if err != nil {
		return nil, fmt.Errorf("decode --remote-target: %w", err)
	}
	deployedRevision, err := DecodeDeployedRevision(deployedRaw)
	if err != nil {
		return nil, fmt.Errorf("decode --deployed-revision: %w", err)
	}
	terminalCleanup, err := DecodeTerminalCleanup(cleanupRaw)
	if err != nil {
		return nil, fmt.Errorf("decode --terminal-cleanup: %w", err)
	}

	receipt, err := Compose(Inputs{
		LocalCheck: localCheck, LocalCheckSHA256: Digest(localCheckRaw), LocalCheckObservedAt: localCheck.GeneratedAt,
		CIWait: ciWait, CIWaitSHA256: Digest(ciWaitRaw), CIWaitObservedAt: ciWait.ObservedAt,
		RemoteTarget: remoteTarget, RemoteTargetSHA256: Digest(remoteTargetRaw), RemoteTargetObservedAt: remoteTarget.ObservedAt,
		DeployedRevision: deployedRevision, DeployedSHA256: Digest(deployedRaw), DeployedObservedAt: deployedRevision.ObservedAt,
		TerminalCleanup: terminalCleanup, CleanupSHA256: Digest(cleanupRaw), CleanupObservedAt: terminalCleanup.GeneratedAt,
	}, service.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("compose graduation receipt: %w", err)
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if paths.Output != "" {
		if err := writeReceipt(paths.Output, raw); err != nil {
			return nil, err
		}
	}
	return raw, nil
}
func (service Observer) ObserveRemoteTarget(ctx context.Context, request RemoteTargetRequest) ([]byte, error) {
	if !graduationRepositoryName(request.Repository) {
		return nil, fmt.Errorf("--repo must be owner/repository")
	}
	if !graduationRemoteName.MatchString(request.Remote) {
		return nil, fmt.Errorf("--remote must be one safe configured remote name")
	}
	if strings.TrimSpace(request.Target) == "" || strings.TrimSpace(request.Target) != request.Target {
		return nil, fmt.Errorf("--target is required without surrounding whitespace")
	}
	resolvePath := service.resolvePath
	if resolvePath == nil {
		resolvePath = filepath.Abs
	}
	absolutePath, err := resolvePath(request.RepositoryPath)
	if err != nil {
		return nil, fmt.Errorf("resolve --repository-path: %w", err)
	}
	if _, err := service.RunGit(ctx, absolutePath, "check-ref-format", "--branch", request.Target); err != nil {
		return nil, fmt.Errorf("--target is not a valid Git branch: %w", err)
	}
	remoteURLRaw, err := service.RunGit(ctx, absolutePath, "remote", "get-url", request.Remote)
	if err != nil {
		return nil, fmt.Errorf("resolve remote %s: %w", request.Remote, err)
	}
	remoteURL := strings.TrimSpace(string(remoteURLRaw))
	if err := ValidateRemoteURL(request.Repository, remoteURL); err != nil {
		return nil, err
	}
	targetRef := "refs/heads/" + request.Target
	observed, err := service.RunGit(ctx, absolutePath, "ls-remote", "--refs", request.Remote, targetRef)
	if err != nil {
		return nil, fmt.Errorf("observe %s %s: %w", request.Remote, targetRef, err)
	}
	line := string(observed)
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) != 2 || fields[1] != targetRef || !gitRevision.MatchString(strings.ToLower(fields[0])) || line != fields[0]+"\t"+targetRef+"\n" {
		return nil, fmt.Errorf("git ls-remote did not return one canonical exact target row")
	}
	evidence := RemoteTargetEvidence{
		SchemaVersion:        SchemaVersion,
		Producer:             RemoteTargetProducer,
		Repository:           request.Repository,
		Remote:               request.Remote,
		RemoteURL:            remoteURL,
		TargetRef:            targetRef,
		Revision:             strings.ToLower(fields[0]),
		ObservedAt:           service.Now().UTC(),
		ObservedOutput:       line,
		ObservedOutputSHA256: Digest(observed),
	}
	raw, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if request.Output != "" {
		if err := writeReceipt(request.Output, raw); err != nil {
			return nil, err
		}
	}
	return raw, nil
}
func readEvidence(path, flag string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("%s is required", flag)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", flag, err)
	}
	defer func() { _ = file.Close() }()
	return readEvidenceFile(file, flag)
}

// evidenceFile keeps metadata and reads bound to the same opened descriptor.
type evidenceFile interface {
	io.Reader
	Stat() (os.FileInfo, error)
}

func readEvidenceFile(file evidenceFile, flag string) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", flag, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must name a regular JSON evidence file", flag)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxEvidenceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", flag, err)
	}
	if len(raw) == 0 || len(raw) > maxEvidenceBytes {
		return nil, fmt.Errorf("%s must contain between 1 and %d bytes", flag, maxEvidenceBytes)
	}
	return raw, nil
}

func writeReceipt(path string, raw []byte) error {
	return writeReceiptInjected(path, raw, nil)
}

// writeReceiptInjected is the file transaction implementation
// (task-9 PR-2): every production call site reaches it only through
// writeReceipt, which always passes a nil *filewrite.Injector,
// so production behaviour is unchanged; a test passes its own Injector
// directly to reach a write/sync/close failure branch deterministically.
func writeReceiptInjected(path string, raw []byte, inj *filewrite.Injector) error {
	directory := filepath.Dir(path)
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		if err != nil {
			return fmt.Errorf("receipt output directory: %w", err)
		}
		return fmt.Errorf("receipt output directory %s is not a directory", directory)
	}
	file, err := filewrite.CreateExclusivePath(path, 0o644, inj)
	if err != nil {
		return fmt.Errorf("create receipt output %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	if err := filewrite.Write(file, raw, path, inj); err != nil {
		return fmt.Errorf("write receipt output %s: %w", path, err)
	}
	if err := filewrite.Sync(file, path, inj); err != nil {
		return fmt.Errorf("sync receipt output %s: %w", path, err)
	}
	return filewrite.Close(file, path, inj)
}

func graduationRepositoryName(value string) bool {
	owner, name, found := strings.Cut(value, "/")
	return found && owner != "" && name != "" && !strings.Contains(name, "/") && !strings.ContainsAny(value, "\r\n ")
}
