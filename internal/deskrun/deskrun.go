// Package deskrun implements `wb desk` (spec/features/desk-connect): the
// browser-approved connection of this machine to Sneat Dev Desk, its status,
// disconnection, and the hand-over of the person's task key.
package deskrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/credentialfile"
	"github.com/sneat-dev/wb/internal/deskclient"
	"github.com/sneat-dev/wb/internal/deskpublish"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// Client is the part of the Desk client these commands use.
type Client interface {
	Start(context.Context, desk.ConnectStartRequest) (desk.ConnectStartResponse, error)
	Token(context.Context, string) (desk.ConnectTokenResponse, error)
	Self(context.Context) (desk.MachineSelf, error)
	Revoke(context.Context) error
}

// Dependencies are the effects the commands have, replaced in tests.
type Dependencies struct {
	ConfigPath  func() string
	NewClient   func(url, credentialFile string) (Client, error)
	MachineName func(configPath string) string
	MachineID   func(projectsRoot string) (string, error)
	OpenBrowser func(string) error
	Restart     func(ctx context.Context, projectsRoot string) error
	Sleep       func(context.Context, time.Duration) error
	Now         func() time.Time
	Random      io.Reader
	WBVersion   string
	OS, Arch    string
}

// DefaultPlatform fills OS and Arch from the running binary.
func DefaultPlatform(deps Dependencies) Dependencies {
	deps.OS, deps.Arch = runtime.GOOS, runtime.GOARCH
	return deps
}

// NewDeskClient is the production NewClient.
func NewDeskClient(url, credentialFile string) (Client, error) {
	return deskclient.New(url, deskclient.Options{CredentialFile: credentialFile})
}

// Sleep waits for d or until ctx ends.
func Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Service runs the commands.
type Service struct {
	deps Dependencies
	exit func(int, string) error
}

// New returns a Service; exit builds WB's coded exit errors.
func New(deps Dependencies, exit func(int, string) error) *Service {
	return &Service{deps: deps, exit: exit}
}

func credentialsDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "credentials")
}

// TaskKeyPath is where the person's task key lives.
func TaskKeyPath(configPath string) string {
	return filepath.Join(credentialsDir(configPath), deskpublish.TaskKeyFile)
}

// ConnectRequest is `wb desk connect`.
type ConnectRequest struct {
	ProjectsRoot  string
	DeskURL       string
	Machine       string
	Detail        desk.Level
	DetailChanged bool
	SpaceHint     string
	NoBrowser     bool
	CanOpen       bool
	NoRestart     bool
	TaskKeyStdin  bool
	Input         io.Reader
	Notice        io.Writer
}

// ConnectResult is what connect reports. It never carries the credential or
// the task key, only the key's public fingerprint.
type ConnectResult struct {
	Machine         string       `json:"machine"`
	MachineID       string       `json:"machine_id"`
	DeskURL         string       `json:"desk_url"`
	PersonalSpaceID string       `json:"personal_space_id"`
	Shares          []desk.Share `json:"shares,omitempty"`
	Detail          desk.Level   `json:"detail"`
	CredentialFile  string       `json:"credential_file"`
	TaskKeyID       string       `json:"task_key_id"`
	TaskKeyCreated  bool         `json:"task_key_created"`
	ConfigPath      string       `json:"config_path"`
	DetailOnly      bool         `json:"detail_only,omitempty"`
	DaemonRestarted bool         `json:"daemon_restarted"`
}

func (s *Service) restart(ctx context.Context, projectsRoot string, skip bool) (bool, error) {
	if skip {
		return false, nil
	}
	if err := s.deps.Restart(ctx, projectsRoot); err != nil {
		return false, fmt.Errorf("desk settings saved, but daemon restart failed: %w", err)
	}
	return true, nil
}

// Connect enrolls this machine (desk-connect#req:device-code-enrollment), or,
// on a connected machine given only --detail, changes its detail level.
func (s *Service) Connect(ctx context.Context, request ConnectRequest) (ConnectResult, error) {
	configPath := s.deps.ConfigPath()
	config, err := wbconfig.LoadDesk(configPath)
	if err != nil {
		return ConnectResult{}, s.exit(2, err.Error())
	}
	if request.DetailChanged && !request.Detail.Valid() {
		return ConnectResult{}, s.exit(2, fmt.Sprintf("--detail must be %q or %q", desk.LevelRepositories, desk.LevelFull))
	}
	if config.Connected() && request.DetailChanged && request.DeskURL == "" && request.SpaceHint == "" && !request.TaskKeyStdin && request.Machine == "" {
		return s.changeDetail(ctx, request, config, configPath)
	}
	deskURL := strings.TrimSpace(request.DeskURL)
	if deskURL == "" {
		deskURL = config.URL
	}
	detail := config.Detail
	if request.DetailChanged {
		detail = request.Detail
	}
	machine := strings.TrimSpace(request.Machine)
	if machine == "" {
		machine = s.deps.MachineName(configPath)
	}
	machineID, err := s.deps.MachineID(request.ProjectsRoot)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("resolve machine id: %w", err)
	}
	start := desk.ConnectStartRequest{SchemaVersion: desk.SchemaVersion, Machine: desk.MachineRef{ID: machineID, Name: machine},
		WBVersion: s.deps.WBVersion, OS: s.deps.OS, Arch: s.deps.Arch, Detail: detail, SpaceHint: strings.TrimSpace(request.SpaceHint)}
	if err := start.Validate(); err != nil {
		return ConnectResult{}, s.exit(2, fmt.Sprintf("cannot connect machine %q: %v (pass a valid --machine)", machine, err))
	}
	key, keyCreated, err := s.taskKey(configPath, request)
	if err != nil {
		return ConnectResult{}, err
	}
	client, err := s.deps.NewClient(deskURL, "")
	if err != nil {
		return ConnectResult{}, s.exit(2, err.Error())
	}
	grant, err := client.Start(ctx, start)
	if err != nil {
		return ConnectResult{}, s.exit(1, "open a Desk connection: "+err.Error())
	}
	_, _ = fmt.Fprintf(request.Notice, "To connect %s, open %s and enter the code %s (expires in %d minutes).\n",
		machine, grant.VerificationURI, grant.UserCode, max(1, grant.ExpiresIn/60))
	if request.CanOpen && !request.NoBrowser {
		if err := s.deps.OpenBrowser(grant.VerificationURIComplete); err != nil {
			_, _ = fmt.Fprintln(request.Notice, "Could not open a browser; open the address above yourself.")
		}
	}
	issued, err := s.poll(ctx, client, grant)
	if err != nil {
		return ConnectResult{}, err
	}
	credentialFile := filepath.Join(credentialsDir(configPath), "desk-"+issued.Machine.Name+".token")
	if err := s.install(ctx, deskURL, credentialFile, issued.Credential); err != nil {
		return ConnectResult{}, err
	}
	shared := make([]string, 0, len(issued.Shares))
	for _, share := range issued.Shares {
		shared = append(shared, share.SpaceID)
	}
	connection := wbconfig.DeskConnection{URL: deskURL, MachineID: issued.Machine.ID, SpaceID: issued.PersonalSpaceID,
		SharedSpaceIDs: shared, CredentialFile: credentialFile, Detail: detail}
	if err := wbconfig.SetDesk(configPath, connection); err != nil {
		return ConnectResult{}, err
	}
	restarted, err := s.restart(ctx, request.ProjectsRoot, request.NoRestart)
	if err != nil {
		return ConnectResult{}, err
	}
	return ConnectResult{Machine: issued.Machine.Name, MachineID: issued.Machine.ID, DeskURL: deskURL,
		PersonalSpaceID: issued.PersonalSpaceID, Shares: issued.Shares, Detail: detail, CredentialFile: credentialFile,
		TaskKeyID: key.ID(), TaskKeyCreated: keyCreated, ConfigPath: configPath, DaemonRestarted: restarted}, nil
}

func (s *Service) changeDetail(ctx context.Context, request ConnectRequest, config wbconfig.DeskConfig, configPath string) (ConnectResult, error) {
	key, created, err := deskpublish.LoadOrCreateTaskKey(TaskKeyPath(configPath), s.deps.Random)
	if err != nil {
		return ConnectResult{}, err
	}
	if err := wbconfig.SetDeskDetail(configPath, request.Detail); err != nil {
		return ConnectResult{}, err
	}
	restarted, err := s.restart(ctx, request.ProjectsRoot, request.NoRestart)
	if err != nil {
		return ConnectResult{}, err
	}
	return ConnectResult{MachineID: config.MachineID, DeskURL: config.URL, PersonalSpaceID: config.SpaceID,
		Detail: request.Detail, CredentialFile: config.CredentialFile, TaskKeyID: key.ID(), TaskKeyCreated: created,
		ConfigPath: configPath, DetailOnly: true, DaemonRestarted: restarted}, nil
}

// taskKey adopts the key piped in with --task-key-stdin, or loads or creates
// this machine's (desk-connect#req:task-keys-correlate-without-names).
func (s *Service) taskKey(configPath string, request ConnectRequest) (deskpublish.TaskKey, bool, error) {
	path := TaskKeyPath(configPath)
	if !request.TaskKeyStdin {
		return deskpublish.LoadOrCreateTaskKey(path, s.deps.Random)
	}
	text, err := credentialfile.ReadToken(request.Input)
	if err != nil {
		return deskpublish.TaskKey{}, false, s.exit(2, "--task-key-stdin: "+err.Error())
	}
	key, err := deskpublish.ParseTaskKey(text)
	if err != nil {
		return deskpublish.TaskKey{}, false, s.exit(2, "--task-key-stdin: "+err.Error())
	}
	if err := deskpublish.InstallTaskKey(path, key); err != nil {
		return deskpublish.TaskKey{}, false, s.exit(1, err.Error()+"; this machine already has a different task key")
	}
	return key, false, nil
}

func (s *Service) poll(ctx context.Context, client Client, grant desk.ConnectStartResponse) (desk.ConnectTokenResponse, error) {
	deadline := s.deps.Now().Add(time.Duration(grant.ExpiresIn) * time.Second)
	interval := time.Duration(grant.Interval) * time.Second
	for {
		if err := s.deps.Sleep(ctx, interval); err != nil {
			return desk.ConnectTokenResponse{}, err
		}
		answer, err := client.Token(ctx, grant.DeviceCode)
		if err != nil {
			return desk.ConnectTokenResponse{}, s.exit(1, "wait for approval: "+err.Error())
		}
		switch answer.Error {
		case "":
			return answer, nil
		case desk.GrantSlowDown:
			interval += 5 * time.Second
		case desk.GrantAccessDenied:
			return desk.ConnectTokenResponse{}, s.exit(1, "the connection was denied in the browser; nothing was changed")
		case desk.GrantExpired:
			return desk.ConnectTokenResponse{}, s.exit(1, "the code expired before it was approved; run wb desk connect again")
		}
		if !s.deps.Now().Before(deadline) {
			return desk.ConnectTokenResponse{}, s.exit(1, "the code expired before it was approved; run wb desk connect again")
		}
	}
}

// install stages the credential, verifies it with Desk, and only then moves
// it into place, so a credential Desk does not accept is never installed.
func (s *Service) install(ctx context.Context, deskURL, credentialFile, credential string) error {
	pending := credentialFile + ".pending"
	if err := credentialfile.ReplacePrivate(pending, credential); err != nil {
		return err
	}
	defer func() { _ = os.Remove(pending) }()
	verifier, err := s.deps.NewClient(deskURL, pending)
	if err != nil {
		return err
	}
	if _, err := verifier.Self(ctx); err != nil {
		return s.exit(1, "verify the Desk credential: "+err.Error())
	}
	if err := os.Rename(pending, credentialFile); err != nil {
		return fmt.Errorf("install the Desk credential: %w", err)
	}
	return nil
}

// DisconnectResult is what disconnect reports.
type DisconnectResult struct {
	Revoked         bool   `json:"revoked"`
	CredentialFile  string `json:"credential_file"`
	ConfigPath      string `json:"config_path"`
	DaemonRestarted bool   `json:"daemon_restarted"`
}

// Disconnect revokes at Desk, then removes the credential and the desk:
// fields (desk-connect#req:disconnect-revokes). When Desk cannot be reached
// it still removes them locally and reports revoked=false.
func (s *Service) Disconnect(ctx context.Context, projectsRoot string, noRestart bool) (DisconnectResult, error) {
	configPath := s.deps.ConfigPath()
	config, err := wbconfig.LoadDesk(configPath)
	if err != nil {
		return DisconnectResult{}, s.exit(2, err.Error())
	}
	if !config.Connected() {
		return DisconnectResult{}, s.exit(1, "this machine is not connected to Desk")
	}
	result := DisconnectResult{CredentialFile: config.CredentialFile, ConfigPath: configPath}
	client, err := s.deps.NewClient(config.URL, config.CredentialFile)
	if err == nil {
		err = client.Revoke(ctx)
	}
	result.Revoked = err == nil || deskclient.CodeOf(err) == desk.ErrorRevoked
	if err := os.Remove(config.CredentialFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("remove the Desk credential: %w", err)
	}
	if err := wbconfig.ClearDesk(configPath); err != nil {
		return result, err
	}
	if result.DaemonRestarted, err = s.restart(ctx, projectsRoot, noRestart); err != nil {
		return result, err
	}
	return result, nil
}

// StatusResult is `wb desk status --json`.
type StatusResult struct {
	Connected       bool           `json:"connected"`
	DeskURL         string         `json:"desk_url,omitempty"`
	MachineID       string         `json:"machine_id,omitempty"`
	PersonalSpaceID string         `json:"personal_space_id,omitempty"`
	Shares          []desk.Share   `json:"shares,omitempty"`
	Detail          desk.Level     `json:"detail,omitempty"`
	LastSnapshotAt  *time.Time     `json:"last_snapshot_at,omitempty"`
	LastHeartbeatAt *time.Time     `json:"last_heartbeat_at,omitempty"`
	DeskError       desk.ErrorCode `json:"desk_error,omitempty"`
	Fix             string         `json:"fix,omitempty"`
}

// Status reads the configuration and Desk's record of this machine. Not
// being connected, or a Desk error, is reported in the result; the command
// turns either into exit 1.
func (s *Service) Status(ctx context.Context) (StatusResult, error) {
	config, err := wbconfig.LoadDesk(s.deps.ConfigPath())
	if err != nil {
		return StatusResult{}, s.exit(2, err.Error())
	}
	if !config.Connected() {
		return StatusResult{Fix: "wb desk connect"}, nil
	}
	result := StatusResult{Connected: true, DeskURL: config.URL, MachineID: config.MachineID,
		PersonalSpaceID: config.SpaceID, Detail: config.Detail}
	client, err := s.deps.NewClient(config.URL, config.CredentialFile)
	var self desk.MachineSelf
	if err == nil {
		self, err = client.Self(ctx)
	}
	if err != nil {
		result.DeskError = deskclient.CodeOf(err)
		if result.DeskError == "" {
			result.DeskError = desk.ErrorUnreachable
		}
		if result.DeskError == desk.ErrorRevoked {
			result.Fix = "wb desk connect"
		}
		return result, nil
	}
	result.PersonalSpaceID, result.Shares = self.PersonalSpaceID, self.Shares
	if !self.LastSnapshotAt.IsZero() {
		result.LastSnapshotAt = &self.LastSnapshotAt
	}
	if !self.LastHeartbeatAt.IsZero() {
		result.LastHeartbeatAt = &self.LastHeartbeatAt
	}
	return result, nil
}

// ExportTaskKey writes the task key for the person's next machine, only to a
// non-terminal stdout.
func (s *Service) ExportTaskKey(out io.Writer, stdoutIsTerminal bool) error {
	if stdoutIsTerminal {
		return s.exit(2, "refusing to print the task key to a terminal; pipe it, for example: wb desk task-key export | ssh vm1 wb desk connect --task-key-stdin")
	}
	path := TaskKeyPath(s.deps.ConfigPath())
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return s.exit(1, "this machine has no Desk task key yet; run wb desk connect first")
	}
	key, _, err := deskpublish.LoadOrCreateTaskKey(path, nil)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, key.Text())
	return err
}
