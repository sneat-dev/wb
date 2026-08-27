package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/graduation"
	"github.com/spf13/cobra"
)

const maxGraduationEvidenceBytes = 4 << 20

var graduationRemoteName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

type graduationCommandDeps struct {
	now    func() time.Time
	runGit func(context.Context, string, ...string) ([]byte, error)
}

func defaultGraduationCommandDeps() graduationCommandDeps {
	return graduationCommandDeps{
		now: func() time.Time { return time.Now().UTC() },
		runGit: func(ctx context.Context, directory string, arguments ...string) ([]byte, error) {
			args := append([]string{"-C", directory}, arguments...)
			return exec.CommandContext(ctx, "git", args...).Output()
		},
	}
}

func newVerifyReceiptCmd() *cobra.Command {
	return newVerifyReceiptCmdWithDeps(defaultGraduationCommandDeps())
}

func newVerifyReceiptCmdWithDeps(deps graduationCommandDeps) *cobra.Command {
	var localCheckPath, ciWaitPath, remoteTargetPath, deployedRevisionPath, terminalCleanupPath, outputPath string
	command := &cobra.Command{
		Use:          "receipt",
		Short:        "Compose exact verification, remote, deployment, and cleanup evidence",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			localCheckRaw, err := readGraduationEvidence(localCheckPath, "--local-check")
			if err != nil {
				return err
			}
			ciWaitRaw, err := readGraduationEvidence(ciWaitPath, "--ci-wait")
			if err != nil {
				return err
			}
			remoteTargetRaw, err := readGraduationEvidence(remoteTargetPath, "--remote-target")
			if err != nil {
				return err
			}
			deployedRaw, err := readGraduationEvidence(deployedRevisionPath, "--deployed-revision")
			if err != nil {
				return err
			}
			cleanupRaw, err := readGraduationEvidence(terminalCleanupPath, "--terminal-cleanup")
			if err != nil {
				return err
			}

			localCheck, err := graduation.DecodeVerificationIndex(localCheckRaw)
			if err != nil {
				return fmt.Errorf("decode --local-check: %w", err)
			}
			ciWait, err := graduation.DecodeCIWaitReceipt(ciWaitRaw)
			if err != nil {
				return fmt.Errorf("decode --ci-wait: %w", err)
			}
			remoteTarget, err := graduation.DecodeRemoteTarget(remoteTargetRaw)
			if err != nil {
				return fmt.Errorf("decode --remote-target: %w", err)
			}
			deployedRevision, err := graduation.DecodeDeployedRevision(deployedRaw)
			if err != nil {
				return fmt.Errorf("decode --deployed-revision: %w", err)
			}
			terminalCleanup, err := graduation.DecodeTerminalCleanup(cleanupRaw)
			if err != nil {
				return fmt.Errorf("decode --terminal-cleanup: %w", err)
			}

			receipt, err := graduation.Compose(graduation.Inputs{
				LocalCheck: localCheck, LocalCheckSHA256: graduation.Digest(localCheckRaw), LocalCheckObservedAt: localCheck.GeneratedAt,
				CIWait: ciWait, CIWaitSHA256: graduation.Digest(ciWaitRaw), CIWaitObservedAt: ciWait.ObservedAt,
				RemoteTarget: remoteTarget, RemoteTargetSHA256: graduation.Digest(remoteTargetRaw), RemoteTargetObservedAt: remoteTarget.ObservedAt,
				DeployedRevision: deployedRevision, DeployedSHA256: graduation.Digest(deployedRaw), DeployedObservedAt: deployedRevision.ObservedAt,
				TerminalCleanup: terminalCleanup, CleanupSHA256: graduation.Digest(cleanupRaw), CleanupObservedAt: terminalCleanup.GeneratedAt,
			}, deps.now().UTC())
			if err != nil {
				return fmt.Errorf("compose graduation receipt: %w", err)
			}
			raw, err := json.MarshalIndent(receipt, "", "  ")
			if err != nil {
				return err
			}
			raw = append(raw, '\n')
			if outputPath != "" {
				if err := writeGraduationReceipt(outputPath, raw); err != nil {
					return err
				}
			}
			_, err = command.OutOrStdout().Write(raw)
			return err
		},
	}
	command.Flags().StringVar(&localCheckPath, "local-check", "", "JSON from wb check --profile ci --format json for one clean exact revision")
	command.Flags().StringVar(&ciWaitPath, "ci-wait", "", "JSON from wb ci wait --json for the exact target revision")
	command.Flags().StringVar(&remoteTargetPath, "remote-target", "", "JSON from wb verify receipt remote-target")
	command.Flags().StringVar(&deployedRevisionPath, "deployed-revision", "", "closed external deployment-provider JSON receipt")
	command.Flags().StringVar(&terminalCleanupPath, "terminal-cleanup", "", "cleanup.json from wb worktree cleanup --apply --remote")
	command.Flags().StringVar(&outputPath, "output", "", "create this new JSON receipt file as well as writing stdout")
	command.AddCommand(newVerifyReceiptRemoteTargetCmd(deps))
	return command
}

func newVerifyReceiptRemoteTargetCmd(deps graduationCommandDeps) *cobra.Command {
	var repository, repositoryPath, remote, target, outputPath string
	command := &cobra.Command{
		Use:          "remote-target",
		Short:        "Observe one exact remote target ref with git ls-remote",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			if !graduationRepositoryName(repository) {
				return fmt.Errorf("--repo must be owner/repository")
			}
			if !graduationRemoteName.MatchString(remote) {
				return fmt.Errorf("--remote must be one safe configured remote name")
			}
			if strings.TrimSpace(target) == "" || strings.TrimSpace(target) != target {
				return fmt.Errorf("--target is required without surrounding whitespace")
			}
			absolutePath, err := filepath.Abs(repositoryPath)
			if err != nil {
				return fmt.Errorf("resolve --repository-path: %w", err)
			}
			if _, err := deps.runGit(command.Context(), absolutePath, "check-ref-format", "--branch", target); err != nil {
				return fmt.Errorf("--target is not a valid Git branch: %w", err)
			}
			remoteURLRaw, err := deps.runGit(command.Context(), absolutePath, "remote", "get-url", remote)
			if err != nil {
				return fmt.Errorf("resolve remote %s: %w", remote, err)
			}
			remoteURL := strings.TrimSpace(string(remoteURLRaw))
			if err := validateGraduationRemoteURL(repository, remoteURL); err != nil {
				return err
			}
			targetRef := "refs/heads/" + target
			observed, err := deps.runGit(command.Context(), absolutePath, "ls-remote", "--refs", remote, targetRef)
			if err != nil {
				return fmt.Errorf("observe %s %s: %w", remote, targetRef, err)
			}
			line := string(observed)
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) != 2 || fields[1] != targetRef || !exactGitObjectID.MatchString(fields[0]) || line != fields[0]+"\t"+targetRef+"\n" {
				return fmt.Errorf("git ls-remote did not return one canonical exact target row")
			}
			evidence := graduation.RemoteTargetEvidence{
				SchemaVersion:        graduation.SchemaVersion,
				Producer:             graduation.RemoteTargetProducer,
				Repository:           repository,
				Remote:               remote,
				RemoteURL:            remoteURL,
				TargetRef:            targetRef,
				Revision:             strings.ToLower(fields[0]),
				ObservedAt:           deps.now().UTC(),
				ObservedOutput:       line,
				ObservedOutputSHA256: graduation.Digest(observed),
			}
			raw, err := json.MarshalIndent(evidence, "", "  ")
			if err != nil {
				return err
			}
			raw = append(raw, '\n')
			if outputPath != "" {
				if err := writeGraduationReceipt(outputPath, raw); err != nil {
					return err
				}
			}
			_, err = command.OutOrStdout().Write(raw)
			return err
		},
	}
	command.Flags().StringVar(&repository, "repo", "", "expected owner/repository identity")
	command.Flags().StringVar(&repositoryPath, "repository-path", ".", "local checkout used to resolve the named remote")
	command.Flags().StringVar(&remote, "remote", "origin", "configured Git remote to observe")
	command.Flags().StringVar(&target, "target", "", "target branch to observe")
	command.Flags().StringVar(&outputPath, "output", "", "create this new JSON evidence file as well as writing stdout")
	return command
}

func readGraduationEvidence(path, flag string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("%s is required", flag)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", flag, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", flag, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must name a regular JSON evidence file", flag)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxGraduationEvidenceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", flag, err)
	}
	if len(raw) == 0 || len(raw) > maxGraduationEvidenceBytes {
		return nil, fmt.Errorf("%s must contain between 1 and %d bytes", flag, maxGraduationEvidenceBytes)
	}
	return raw, nil
}

func writeGraduationReceipt(path string, raw []byte) error {
	directory := filepath.Dir(path)
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		if err != nil {
			return fmt.Errorf("receipt output directory: %w", err)
		}
		return fmt.Errorf("receipt output directory %s is not a directory", directory)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create receipt output %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(raw); err != nil {
		return fmt.Errorf("write receipt output %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync receipt output %s: %w", path, err)
	}
	return file.Close()
}

func graduationRepositoryName(value string) bool {
	owner, name, found := strings.Cut(value, "/")
	return found && owner != "" && name != "" && !strings.Contains(name, "/") && !strings.ContainsAny(value, "\r\n ")
}

func validateGraduationRemoteURL(repository, remoteURL string) error {
	if remoteURL == "" || strings.ContainsAny(remoteURL, "\r\n") {
		return fmt.Errorf("remote URL is empty or multiline")
	}
	if strings.Contains(remoteURL, "://") {
		parsed, err := url.Parse(remoteURL)
		if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") {
			return fmt.Errorf("remote URL must identify github.com")
		}
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword || parsed.Scheme == "http" || parsed.Scheme == "https" {
				return fmt.Errorf("remote URL must not embed credentials")
			}
		}
	} else {
		colon := strings.IndexByte(remoteURL, ':')
		if colon < 0 || !strings.HasSuffix(strings.ToLower(remoteURL[:colon]), "@github.com") {
			return fmt.Errorf("remote URL must identify github.com")
		}
	}
	normalized := strings.TrimSuffix(strings.TrimSuffix(remoteURL, "/"), ".git")
	if at := strings.LastIndex(normalized, ":"); at >= 0 && !strings.Contains(normalized[at+1:], "/../") {
		prefix := normalized[:at]
		if strings.Contains(prefix, "@") && !strings.Contains(normalized[at+1:], "//") {
			normalized = normalized[at+1:]
		}
	}
	normalized = strings.Trim(normalized, "/")
	parts := strings.Split(normalized, "/")
	if len(parts) < 2 || parts[len(parts)-2]+"/"+parts[len(parts)-1] != repository {
		return fmt.Errorf("remote URL does not identify expected repository %s", repository)
	}
	return nil
}
