// Package cockpitrun owns Cockpit open/export operations without CLI dependencies.
package cockpitrun

import (
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"net/http"
)

type LocalRequest struct {
	Root, Listen string
	Mint         bool
}
type LoginOperation func(context.Context, *http.Client) (cockpit.LoginCode, string, error)
type LocalService struct {
	deps  daemonruntime.Dependencies
	login LoginOperation
}

func NewLocalService(deps daemonruntime.Dependencies, login LoginOperation) LocalService {
	return LocalService{deps: deps, login: login}
}

// LocalSession is what a local invocation learns from the daemon: its
// listen address, a non-fatal warning from the start-or-reuse path, and, when
// one was requested, the login code, the path it is redeemed at and the
// session key of the session the code starts (cockpit#req:session-key).
type LocalSession struct {
	Listen  string
	Warning string
	Code    string
	Path    string
	Key     string
}

// ErrNoSessionKey is the refusal of a login against a daemon that
// minted a code and no session key: it is an older wb than this one, still
// running from before an update, and a session it starts would make the cookie
// alone an owner (cockpit#req:session-key). Nothing is printed or opened.
var ErrNoSessionKey = errors.New("request a cockpit login code: the running daemon is an older wb that issues no Cockpit session key; restart it with `wb daemon restart`, then run `wb cockpit` again")

// cockpitLocalFromDaemon starts or reuses this machine's daemon the way
// `wb dashboard --local` does and, when mint is set, asks it for a login code
// on the owner channel (the unix socket behind the owner bearer token).
func (service LocalService) Local(ctx context.Context, request LocalRequest) (LocalSession, error) {
	deps := service.deps
	root, listen, mint := request.Root, request.Listen, request.Mint
	controller := daemonruntime.NewController(deps, root)
	// `wb cockpit` opens the daemon this machine has; it never moves one. A live
	// daemon recorded on another address is reused when --listen was not given
	// (listen is empty) and is a refusal when it was, because Start would
	// otherwise replace it (rewriting the launchd job on macOS, or overwriting
	// its record and spawning a second process elsewhere).
	recorded, found, err := controller.LoadState()
	if err != nil {
		return LocalSession{}, fmt.Errorf("read the local daemon record: %w", err)
	}
	switch {
	case found && recorded.Status != daemon.StatusStopped && recorded.PID > 0 && deps.Alive(recorded.PID):
		if listen == "" {
			listen = recorded.Listen
		} else if listen != recorded.Listen {
			return LocalSession{}, fmt.Errorf(
				"a local daemon is already running on %s, not the requested %s; use `wb cockpit --listen %s`, or stop it first with `wb daemon stop`",
				recorded.Listen, listen, recorded.Listen)
		}
	case listen == "":
		listen = daemonruntime.DefaultListen
	}
	result, err := controller.Start(ctx, listen)
	if err != nil {
		return LocalSession{}, fmt.Errorf("start local daemon: %w", err)
	}
	session := LocalSession{Listen: result.State.Listen, Warning: result.Warning}
	if !mint {
		return session, nil
	}
	client, err := cockpitOwnerClient(result, controller.LoadState, deps, root)
	if err != nil {
		return LocalSession{}, err
	}
	issued, path, err := service.login(ctx, client)
	if err != nil {
		return LocalSession{}, fmt.Errorf("request a cockpit login code: %w", err)
	}
	if issued.Code == "" {
		return LocalSession{}, errors.New("request a cockpit login code: the daemon returned no code")
	}
	if issued.Key == "" {
		return LocalSession{}, ErrNoSessionKey
	}
	if path != cockpit.LoginPath {
		return LocalSession{}, fmt.Errorf("request a cockpit login code: the daemon named the login path %q, want %q", path, cockpit.LoginPath)
	}
	session.Code, session.Path, session.Key = issued.Code, path, issued.Key
	return session, nil
}

// cockpitOwnerClient builds the owner-channel client against the daemon that
// Start just brought up, so the command starts it exactly once. It keeps the
// readiness check `wb peers` applies to the same state.
func cockpitOwnerClient(result daemonruntime.Result, load func() (daemon.State, bool, error), deps daemonruntime.Dependencies, root string) (*http.Client, error) {
	state, found, err := load()
	if err != nil {
		return nil, err
	}
	if !found || state.Status != daemon.StatusReady || !result.ProcessManagerRunning {
		return nil, errors.New("local daemon is not ready")
	}
	localClient := deps.LocalClient
	if localClient == nil {
		localClient = daemonruntime.LocalHTTPClient
	}
	httpClient, err := localClient(root, state.OwnerToken)
	if err != nil {
		return nil, fmt.Errorf("open the owner channel to the local daemon: %w", err)
	}
	return httpClient, nil
}
