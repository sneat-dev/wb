package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/remotestate/hub"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

const defaultRemoteHubURL = "https://wb-github-app.sneat.dev"

type remoteEnrollDeps struct {
	configPath func() string
	verify     func(context.Context, string, string, string) error
	restart    func(context.Context, string) error
}

func defaultRemoteEnrollDeps() remoteEnrollDeps {
	return remoteEnrollDeps{
		configPath: wbconfig.DefaultPath,
		verify: func(ctx context.Context, hubURL, machine, token string) error {
			provider, err := hub.New(hub.Options{BaseURL: hubURL, Machine: machine, Token: token})
			if err != nil {
				return err
			}
			_, err = provider.List(ctx)
			return err
		},
		restart: restartDaemonAfterRemoteEnroll,
	}
}

type remoteEnrollResult struct {
	Machine       string `json:"machine"`
	HubURL        string `json:"hub_url"`
	ConfigPath    string `json:"config_path"`
	TokenFile     string `json:"token_file"`
	Verified      bool   `json:"verified"`
	DaemonRestart bool   `json:"daemon_restart"`
}

func newRemoteEnrollCmd() *cobra.Command {
	var machine, hubURL, tokenFile string
	var tokenStdin, restartDaemon, jsonOut bool
	command := &cobra.Command{
		Use:   "enroll",
		Short: "Securely enroll this machine with the Workbench event hub",
		Long: `Reads the one-time machine credential only from explicitly selected stdin,
verifies it against the hub, stores it in a private file, updates the remote
section of wb.yaml without replacing unrelated settings, and restarts a running
WB daemon so event polling begins immediately.

The credential is never accepted in argv and is never printed or recorded in
WB command telemetry. From the dashboard, copy the credential and run:

  pbpaste | wb remote enroll --machine studio-mac --token-stdin`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runRemoteEnroll(command.Context(), defaultRemoteEnrollDeps(), projectsRoot, machine, hubURL, tokenFile, tokenStdin, restartDaemon, jsonOut, command.InOrStdin(), command.OutOrStdout())
		},
	}
	command.Flags().StringVar(&machine, "machine", "", "unique name for this machine (required)")
	command.Flags().StringVar(&hubURL, "url", defaultRemoteHubURL, "Workbench event hub HTTPS origin")
	command.Flags().StringVar(&tokenFile, "token-file", "", "private destination for the machine credential (default: managed file beside wb.yaml)")
	command.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the machine credential from stdin")
	command.Flags().BoolVar(&restartDaemon, "restart-daemon", true, "restart WB daemon if it is running")
	addJSONFormatFlags(command, &jsonOut)
	return command
}

func runRemoteEnroll(ctx context.Context, deps remoteEnrollDeps, projectsRoot, machine, hubURL, tokenFile string, tokenStdin, restartDaemon, jsonOut bool, in io.Reader, out io.Writer) error {
	machine = strings.TrimSpace(machine)
	if machine == "" {
		return &exitError{code: exitUsage, message: "--machine is required"}
	}
	if !tokenStdin {
		return &exitError{code: exitUsage, message: "--token-stdin is required; WB never accepts a machine credential in argv"}
	}
	token, err := readRemoteEnrollmentToken(in)
	if err != nil {
		return &exitError{code: exitUsage, message: err.Error()}
	}
	if err := deps.verify(ctx, hubURL, machine, token); err != nil {
		return &exitError{code: exitFindings, message: "verify hub credential: " + err.Error()}
	}
	configPath := deps.configPath()
	if tokenFile == "" {
		digest := sha256.Sum256([]byte(token))
		tokenFile = filepath.Join(filepath.Dir(configPath), "credentials", "hub-"+machine+"-"+hex.EncodeToString(digest[:6])+".token")
	}
	absoluteTokenFile, err := filepath.Abs(tokenFile)
	if err != nil {
		return fmt.Errorf("resolve token file: %w", err)
	}
	created, err := writePrivateCredential(absoluteTokenFile, token)
	if err != nil {
		return err
	}
	if err := wbconfig.SetRemoteHub(configPath, hubURL, machine, absoluteTokenFile); err != nil {
		if created {
			_ = os.Remove(absoluteTokenFile)
		}
		return err
	}
	restarted := false
	if restartDaemon {
		if err := deps.restart(ctx, projectsRoot); err != nil {
			return fmt.Errorf("hub enrollment saved, but daemon restart failed: %w", err)
		}
		restarted = true
	}
	result := remoteEnrollResult{Machine: machine, HubURL: hubURL, ConfigPath: configPath, TokenFile: absoluteTokenFile, Verified: true, DaemonRestart: restarted}
	if jsonOut {
		return json.NewEncoder(out).Encode(result)
	}
	_, err = fmt.Fprintf(out, "Enrolled %s\n  hub         %s\n  config      %s\n  credential  %s\n  verified    yes\n  daemon      %s\n", machine, hubURL, configPath, absoluteTokenFile, map[bool]string{true: "restarted if running", false: "restart skipped"}[restarted])
	return err
}

func readRemoteEnrollmentToken(in io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(in, 16<<10+1))
	if err != nil {
		return "", fmt.Errorf("read machine credential from stdin: %w", err)
	}
	if len(raw) > 16<<10 {
		return "", errors.New("machine credential from stdin exceeds 16384 bytes")
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("machine credential from stdin must contain one non-empty token")
	}
	return token, nil
}

func writePrivateCredential(path, token string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create credential directory: %w", err)
	}
	if existing, err := os.ReadFile(path); err == nil {
		if strings.TrimSpace(string(existing)) != token {
			return false, fmt.Errorf("credential file %s already exists with different contents; choose a new --token-file", path)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return false, fmt.Errorf("protect credential file: %w", err)
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect credential file: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("create credential file: %w", err)
	}
	if _, err := io.WriteString(file, token+"\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return false, fmt.Errorf("write credential file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return false, fmt.Errorf("sync credential file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("close credential file: %w", err)
	}
	return true, nil
}

func restartDaemonAfterRemoteEnroll(ctx context.Context, projectsRoot string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, executable, "--projects-root", projectsRoot, "--non-interactive", "daemon", "restart", "--if-running", "--format=json") //nolint:gosec // current verified executable.
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
