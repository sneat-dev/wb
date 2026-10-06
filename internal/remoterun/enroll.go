package remoterun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/credentialfile"
	"github.com/sneat-dev/wb/internal/remotestate/hub"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

type EnrollDependencies struct {
	ConfigPath func() string
	Verify     func(context.Context, string, string, string) error
	Restart    func(context.Context, string) error
}
type EnrollResult struct {
	Machine       string `json:"machine"`
	HubURL        string `json:"hub_url"`
	ConfigPath    string `json:"config_path"`
	TokenFile     string `json:"token_file"`
	Verified      bool   `json:"verified"`
	DaemonRestart bool   `json:"daemon_restart"`
}

func DefaultEnrollDependencies() EnrollDependencies {
	return EnrollDependencies{
		ConfigPath: wbconfig.DefaultPath,
		Verify: func(ctx context.Context, hubURL, machine, token string) error {
			provider, err := hub.New(hub.Options{BaseURL: hubURL, Machine: machine, Token: token})
			if err != nil {
				return err
			}
			_, err = provider.List(ctx)
			return err
		},
		Restart: RestartDaemonAfterEnroll,
	}
}
func runRemoteEnroll(ctx context.Context, deps EnrollDependencies, exitFactory func(int, string) error, projectsRoot, machine, hubURL, tokenFile string, tokenStdin, restartDaemon bool, in io.Reader, abs func(string) (string, error)) (EnrollResult, error) {
	machine = strings.TrimSpace(machine)
	if machine == "" {
		return EnrollResult{}, exitFactory(2, "--machine is required")
	}
	if !tokenStdin {
		return EnrollResult{}, exitFactory(2, "--token-stdin is required; WB never accepts a machine credential in argv")
	}
	token, err := credentialfile.ReadToken(in)
	if err != nil {
		return EnrollResult{}, exitFactory(2, err.Error())
	}
	if err := deps.Verify(ctx, hubURL, machine, token); err != nil {
		return EnrollResult{}, exitFactory(1, "verify hub credential: "+err.Error())
	}
	configPath := deps.ConfigPath()
	if tokenFile == "" {
		digest := sha256.Sum256([]byte(token))
		tokenFile = filepath.Join(filepath.Dir(configPath), "credentials", "hub-"+machine+"-"+hex.EncodeToString(digest[:6])+".token")
	}
	absoluteTokenFile, err := abs(tokenFile)
	if err != nil {
		return EnrollResult{}, fmt.Errorf("resolve token file: %w", err)
	}
	created, err := credentialfile.WritePrivate(absoluteTokenFile, token)
	if err != nil {
		return EnrollResult{}, err
	}
	if err := wbconfig.SetRemoteHub(configPath, hubURL, machine, absoluteTokenFile); err != nil {
		if created {
			_ = os.Remove(absoluteTokenFile)
		}
		return EnrollResult{}, err
	}
	restarted := false
	if restartDaemon {
		if err := deps.Restart(ctx, projectsRoot); err != nil {
			return EnrollResult{}, fmt.Errorf("hub enrollment saved, but daemon restart failed: %w", err)
		}
		restarted = true
	}
	result := EnrollResult{Machine: machine, HubURL: hubURL, ConfigPath: configPath, TokenFile: absoluteTokenFile, Verified: true, DaemonRestart: restarted}
	return result, nil
}
func RestartDaemonAfterEnroll(ctx context.Context, projectsRoot string) error {
	return restartDaemonAfterEnroll(ctx, projectsRoot, os.Executable, func(ctx context.Context, executable string, args []string) ([]byte, error) {
		return exec.CommandContext(ctx, executable, args...).CombinedOutput() //nolint:gosec // current verified executable.
	})
}

func restartDaemonAfterEnroll(ctx context.Context, projectsRoot string, executable func() (string, error), run func(context.Context, string, []string) ([]byte, error)) error {
	path, err := executable()
	if err != nil {
		return err
	}
	output, err := run(ctx, path, []string{"--projects-root", projectsRoot, "--non-interactive", "daemon", "restart", "--if-running", "--format=json"})
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

type EnrollRequest struct {
	ProjectsRoot, Machine, HubURL, TokenFile string
	TokenStdin, RestartDaemon                bool
	Input                                    io.Reader
}
type EnrollService struct {
	deps        EnrollDependencies
	exitFactory func(int, string) error
	abs         func(string) (string, error)
}

func NewEnroll(deps EnrollDependencies, exitFactory func(int, string) error) *EnrollService {
	return &EnrollService{deps: deps, exitFactory: exitFactory, abs: filepath.Abs}
}
func (s *EnrollService) Enroll(ctx context.Context, req EnrollRequest) (EnrollResult, error) {
	return runRemoteEnroll(ctx, s.deps, s.exitFactory, req.ProjectsRoot, req.Machine, req.HubURL, req.TokenFile, req.TokenStdin, req.RestartDaemon, req.Input, s.abs)
}
