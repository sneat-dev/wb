package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	codeGrapherBinary       = "codegrapher"
	codeGrapherRepository   = "github.com/code-grapher/codegrapher"
	codeGrapherGoModule     = "github.com/specscore/codegrapher/cmd/codegrapher"
	codeGrapherHomebrewCask = "code-grapher/tap/codegrapher"
)

// codeGrapherDeps keeps host inspection and process execution replaceable. The
// command is deliberately a small adapter: CodeGrapher owns its release
// artifacts, while WB owns only deterministic local-tool orchestration.
type codeGrapherDeps struct {
	goos         string
	lookPath     func(string) (string, error)
	evalSymlinks func(string) (string, error)
	run          func(context.Context, string, ...string) ([]byte, error)
}

func defaultCodeGrapherDeps() codeGrapherDeps {
	return codeGrapherDeps{
		goos:         runtime.GOOS,
		lookPath:     exec.LookPath,
		evalSymlinks: filepath.EvalSymlinks,
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // commands are fixed below
		},
	}
}

// codeGrapherStatus is deliberately based on the installed binary's own
// version command rather than a package-manager database. A cask may be stale
// or a launcher may point somewhere unexpected; the executable is the thing an
// agent will actually invoke.
type codeGrapherStatus struct {
	SchemaVersion int       `json:"schema_version"`
	ObservedAt    time.Time `json:"observed_at"`
	Installed     bool      `json:"installed"`
	Runnable      bool      `json:"runnable"`
	Binary        string    `json:"binary"`
	ResolvedPath  string    `json:"resolved_path,omitempty"`
	Version       string    `json:"version,omitempty"`
	Commit        string    `json:"commit,omitempty"`
	Built         string    `json:"built,omitempty"`
	Manager       string    `json:"manager,omitempty"`
	Repository    string    `json:"repository"`
	Module        string    `json:"module"`
	Platform      string    `json:"platform"`
	Detail        string    `json:"detail,omitempty"`
}

func newCodeGrapherCmd() *cobra.Command {
	return newCodeGrapherCmdWithDeps(defaultCodeGrapherDeps())
}

func newCodeGrapherCmdWithDeps(deps codeGrapherDeps) *cobra.Command {
	command := &cobra.Command{
		Use:   "codegrapher",
		Short: "Install, update, and inspect the local CodeGrapher CLI",
		Long: `Manage the local CodeGrapher CLI without coupling a WB operation to graph refresh.

macOS and Linux use the published Homebrew cask. Windows uses the published Go
module because CodeGrapher has no WinGet or Scoop package. Each completed
install or update re-probes the executable and reports its exact installed
version and provenance. Use --format=json for scripts; --json remains a
compatibility shortcut.`,
	}
	command.AddCommand(newCodeGrapherStatusCmd(deps))
	command.AddCommand(newCodeGrapherInstallCmd(deps, "install"))
	command.AddCommand(newCodeGrapherInstallCmd(deps, "update"))
	setDiscoveryTerms(command, "codegrapher graph code intelligence install update status tool cli")
	return command
}

func newCodeGrapherStatusCmd(deps codeGrapherDeps) *cobra.Command {
	var format string
	var jsonShortcut bool
	command := &cobra.Command{
		Use:   "status",
		Short: "Report the installed CodeGrapher binary and exact provenance",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := codeGrapherFormat(format, jsonShortcut)
			if err != nil {
				return err
			}
			status := inspectCodeGrapher(command.Context(), deps)
			if err := writeCodeGrapherStatus(command, status, format); err != nil {
				return err
			}
			if !status.Runnable {
				return &exitError{code: exitFindings, message: "CodeGrapher is not installed; run `wb codegrapher install --yes`"}
			}
			return nil
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonShortcut, "json", false, "shortcut for --format=json")
	return command
}

func newCodeGrapherInstallCmd(deps codeGrapherDeps, verb string) *cobra.Command {
	var format, version string
	var jsonShortcut, yes, dryRun bool
	command := &cobra.Command{
		Use:   verb,
		Short: map[string]string{"install": "Install the CodeGrapher CLI", "update": "Update the CodeGrapher CLI"}[verb],
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := codeGrapherFormat(format, jsonShortcut)
			if err != nil {
				return err
			}
			if !yes && !dryRun {
				return &exitError{code: exitUsage, message: "refusing to modify this machine without --yes; preview with --dry-run"}
			}
			result, err := runCodeGrapherInstall(command.Context(), deps, verb, version, dryRun)
			if err != nil {
				return err
			}
			if !dryRun {
				status := inspectCodeGrapher(command.Context(), deps)
				result.Status = &status
				if !result.Status.Runnable {
					return &exitError{code: exitFindings, message: "installer completed but CodeGrapher could not be re-probed on PATH"}
				}
			}
			return writeCodeGrapherInstall(command, result, format)
		},
	}
	command.Flags().StringVar(&version, "version", "latest", "CodeGrapher release for Windows Go installation; latest by default")
	command.Flags().BoolVar(&yes, "yes", false, "approve the package-manager or Go installation command")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "print the exact command without executing it")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonShortcut, "json", false, "shortcut for --format=json")
	return command
}

type codeGrapherInstallResult struct {
	SchemaVersion int                `json:"schema_version"`
	Action        string             `json:"action"`
	Platform      string             `json:"platform"`
	Commands      [][]string         `json:"commands"`
	DryRun        bool               `json:"dry_run"`
	Status        *codeGrapherStatus `json:"status,omitempty"`
}

func runCodeGrapherInstall(ctx context.Context, deps codeGrapherDeps, verb, version string, dryRun bool) (codeGrapherInstallResult, error) {
	result := codeGrapherInstallResult{SchemaVersion: 1, Action: verb, Platform: deps.goos, DryRun: dryRun}
	if version == "" {
		version = "latest"
	}
	switch deps.goos {
	case "darwin", "linux":
		if version != "latest" {
			return result, &exitError{code: exitUsage, message: "--version is unavailable for the Homebrew cask; use latest or install that release directly"}
		}
		if _, err := deps.lookPath("brew"); err != nil {
			return result, &exitError{code: exitUsage, message: "CodeGrapher installation on " + deps.goos + " requires Homebrew; install Homebrew then rerun this command"}
		}
		args := []string{"install", "--cask", codeGrapherHomebrewCask}
		if verb == "update" {
			args = []string{"upgrade", "--cask", "codegrapher"}
		}
		result.Commands = [][]string{{"brew", "update"}, append([]string{"brew"}, args...)}
		if dryRun {
			return result, nil
		}
		if output, err := deps.run(ctx, "brew", "update"); err != nil {
			return result, commandFailure("brew update", output, err)
		}
		if output, err := deps.run(ctx, "brew", args...); err != nil {
			return result, commandFailure("brew "+strings.Join(args, " "), output, err)
		}
	case "windows":
		if _, err := deps.lookPath("go"); err != nil {
			return result, &exitError{code: exitUsage, message: "CodeGrapher installation on Windows requires Go because no WinGet or Scoop package is published"}
		}
		result.Commands = [][]string{{"go", "install", codeGrapherGoModule + "@" + version}}
		if dryRun {
			return result, nil
		}
		if output, err := deps.run(ctx, "go", "install", codeGrapherGoModule+"@"+version); err != nil {
			return result, commandFailure("go install "+codeGrapherGoModule+"@"+version, output, err)
		}
	default:
		return result, &exitError{code: exitUsage, message: "CodeGrapher installation is unsupported on " + deps.goos}
	}
	return result, nil
}

func commandFailure(command string, output []byte, err error) error {
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("%s: %w", command, err)
	}
	return fmt.Errorf("%s: %w: %s", command, err, detail)
}

func inspectCodeGrapher(ctx context.Context, deps codeGrapherDeps) codeGrapherStatus {
	status := codeGrapherStatus{SchemaVersion: 1, ObservedAt: time.Now().UTC(), Binary: codeGrapherBinary, Repository: codeGrapherRepository, Module: codeGrapherGoModule, Platform: deps.goos}
	path, err := deps.lookPath(codeGrapherBinary)
	if err != nil {
		status.Detail = "not found on PATH"
		return status
	}
	status.Installed = true
	status.ResolvedPath = path
	if resolved, resolveErr := deps.evalSymlinks(path); resolveErr == nil {
		status.ResolvedPath = resolved
	}
	status.Manager = codeGrapherManager(status.ResolvedPath)
	output, err := deps.run(ctx, path, "version")
	if err != nil {
		status.Detail = "version probe failed: " + strings.TrimSpace(string(output))
		return status
	}
	fields := strings.Fields(string(output))
	switch {
	case len(fields) == 1:
		// CodeGrapher's earlier builds exposed the same exact version through a
		// bare `version` response. Keep those installations observable while
		// leaving commit/build fields honestly absent.
		status.Version = fields[0]
	case len(fields) >= 2 && fields[0] == codeGrapherBinary:
		status.Version = fields[1]
		if len(fields) >= 3 {
			status.Commit = strings.Trim(fields[2], "()")
		}
		if len(fields) >= 4 {
			status.Built = fields[3]
		}
	}
	if status.Version == "" {
		status.Detail = "version probe returned an unrecognized response"
		return status
	}
	status.Runnable = true
	return status
}

func codeGrapherManager(path string) string {
	normalized := strings.ToLower(filepath.ToSlash(path))
	if strings.Contains(normalized, "/caskroom/") || strings.Contains(normalized, "/cellar/") || strings.Contains(normalized, "/linuxbrew/") {
		return "homebrew"
	}
	return "manual"
}

func codeGrapherFormat(format string, jsonShortcut bool) (string, error) {
	if jsonShortcut {
		if format != "text" && format != "json" {
			return "", fmt.Errorf("--json cannot be combined with --format=%s", format)
		}
		format = "json"
	}
	if err := requireOutputFormat(format, "text", "json"); err != nil {
		return "", err
	}
	return format, nil
}

func writeCodeGrapherStatus(command *cobra.Command, status codeGrapherStatus, format string) error {
	if format == "json" {
		return json.NewEncoder(command.OutOrStdout()).Encode(status)
	}
	if !status.Installed {
		_, err := fmt.Fprintln(command.OutOrStdout(), "CodeGrapher: not installed")
		return err
	}
	if !status.Runnable {
		_, err := fmt.Fprintf(command.OutOrStdout(), "CodeGrapher: found but not runnable\npath: %s\ndetail: %s\n", status.ResolvedPath, status.Detail)
		return err
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "CodeGrapher %s\npath: %s\nmanager: %s\nrepository: %s\nmodule: %s\n", status.Version, status.ResolvedPath, status.Manager, status.Repository, status.Module)
	return err
}

func writeCodeGrapherInstall(command *cobra.Command, result codeGrapherInstallResult, format string) error {
	if format == "json" {
		return json.NewEncoder(command.OutOrStdout()).Encode(result)
	}
	if result.DryRun {
		steps := make([]string, 0, len(result.Commands))
		for _, step := range result.Commands {
			steps = append(steps, strings.Join(step, " "))
		}
		_, err := fmt.Fprintf(command.OutOrStdout(), "dry run: %s\n", strings.Join(steps, " && "))
		return err
	}
	if result.Status == nil {
		return fmt.Errorf("CodeGrapher installer completed without a verification result")
	}
	return writeCodeGrapherStatus(command, *result.Status, "text")
}
