package peersrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

func (service Service) isHubConfigured() (bool, error) {
	_, found, err := hubconfig.Load(service.deps.ConfigPath())
	return found, err
}
func (service Service) requireHubConfigured() error {
	found, err := service.isHubConfigured()
	if err != nil {
		return err
	}
	if !found {
		return refusal(Usage, "no hub is configured on this machine; `wb peers invite`, `block`, `unblock` and `disconnect` (for a downstream peer) require a self-hosted hub — see `hub:` in wb.yaml, or run `wb peers join` to become a peer of one instead")
	}
	return nil
}

func (service Service) Invite(ctx context.Context, request InviteRequest) (InviteResult, error) {
	if err := service.requireHubConfigured(); err != nil {
		return InviteResult{}, err
	}
	var path string
	if strings.TrimSpace(request.TokenFile) != "" {
		var err error
		path, err = service.deps.Abs(request.TokenFile)
		if err != nil {
			return InviteResult{}, fmt.Errorf("resolve token file: %w", err)
		}
		if err := refuseExistingTokenFile(path); err != nil {
			return InviteResult{}, err
		}
	}
	admin, err := service.deps.Admin(ctx, request.Root)
	if err != nil {
		return InviteResult{}, err
	}
	response, err := admin.Invite(ctx, peers.InviteRequest{Name: request.Name, Rotate: request.Rotate})
	if err != nil {
		return InviteResult{}, err
	}
	result := InviteResult{Output: InviteOutput{PeerID: response.PeerID, Name: response.Name, Token: response.Token, CreatedAt: response.CreatedAt, Rotated: response.Rotated}, AttemptedTokenFile: path}
	if path != "" {
		if err := writeOneTimeToken(path, response.Token); err != nil {
			result.TokenWriteError = err
			return result, nil
		}
		result.Output.Token = ""
		result.Output.TokenFile = path
	}
	return result, nil
}
func (service Service) TrustChange(ctx context.Context, request TrustRequest) (TrustResult, error) {
	if result, handled, err := service.upstreamTrustChange(request); handled {
		return result, err
	}
	if err := service.requireHubConfigured(); err != nil {
		return TrustResult{}, err
	}
	admin, err := service.deps.Admin(ctx, request.Root)
	if err != nil {
		return TrustResult{}, err
	}
	response, err := admin.Trust(ctx, request.Action, request.Peer)
	return TrustResult{Response: response}, err
}
func (service Service) Disconnect(ctx context.Context, request DisconnectRequest) (peers.DisconnectResponse, error) {
	if err := service.requireHubConfigured(); err != nil {
		return peers.DisconnectResponse{}, err
	}
	admin, err := service.deps.Admin(ctx, request.Root)
	if err != nil {
		return peers.DisconnectResponse{}, err
	}
	return admin.Disconnect(ctx, request.Peer)
}

func (service Service) resolveUpstreamRow(root string) (peers.Record, bool, error) {
	upstream, found, err := wbconfig.LoadPeersUpstream(service.deps.ConfigPath())
	if err != nil || !found {
		return peers.Record{}, false, err
	}
	path, err := peerUpstreamStatePath(root)
	if err != nil {
		return peers.Record{}, false, err
	}
	state, err := loadPeerUpstreamState(path)
	if err != nil {
		return peers.Record{}, false, err
	}
	status := "offline"
	if state.Blocked {
		status = "blocked"
	}
	name := upstreamDisplayName(upstream.URL)
	return peers.Record{SchemaVersion: peers.SchemaVersion, ID: name, Name: name, Role: "upstream", Status: status}, true, nil
}
func (service Service) List(ctx context.Context, request ListRequest, warn func(error)) (ListResult, error) {
	list, downstreamErr := service.readPeersList(ctx, request.Root)
	rows := append([]peers.Record(nil), list.Peers...)
	upstream, found, err := service.resolveUpstreamRow(request.Root)
	if err != nil {
		return ListResult{}, err
	}
	if found {
		rows = append(rows, upstream)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	result := ListResult{Response: peers.ListResponse{SchemaVersion: peers.SchemaVersion, Peers: rows}}
	if downstreamErr != nil {
		warn(downstreamErr)
		configured, err := service.isHubConfigured()
		if err != nil {
			return result, err
		}
		if configured {
			result.Finding = refusal(Findings, "downstream peers are unavailable (see stderr); the local daemon may be stopped or unhealthy")
		}
	}
	return result, nil
}
func (service Service) Get(ctx context.Context, request GetRequest) (peers.Detail, error) {
	if strings.EqualFold(request.Peer, "upstream") {
		row, found, err := service.resolveUpstreamRow(request.Root)
		if err != nil {
			return peers.Detail{}, err
		}
		if found {
			return peers.Detail{Record: row}, nil
		}
		return peers.Detail{}, refusal(Findings, "no upstream is configured; run `wb peers join <hub-url>`")
	}
	listen, err := service.deps.ListenAddress(request.Root)
	if err != nil {
		return peers.Detail{}, refusal(Findings, err.Error())
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listen+"/api/v1/peers/"+url.PathEscape(request.Peer), nil)
	if err != nil {
		return peers.Detail{}, err
	}
	response, err := service.deps.Do(httpRequest)
	if err != nil {
		return peers.Detail{}, refusal(Findings, "read local daemon peers API: "+err.Error())
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return peers.Detail{}, refusal(Findings, fmt.Sprintf("no peer named or with ID %q", request.Peer))
	}
	if response.StatusCode != http.StatusOK {
		return peers.Detail{}, refusal(Findings, "read local daemon peers API: "+response.Status)
	}
	var detail peers.Detail
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&detail); err != nil {
		return peers.Detail{}, err
	}
	return detail, nil
}

var errPeersDownstreamUnavailable = errors.New("downstream peers are unavailable")

func (service Service) readPeersList(ctx context.Context, root string) (peers.ListResponse, error) {
	empty := peers.ListResponse{SchemaVersion: peers.SchemaVersion}
	listen, err := service.deps.ListenAddress(root)
	if err != nil {
		return empty, fmt.Errorf("%w: %v", errPeersDownstreamUnavailable, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listen+"/api/v1/peers", nil)
	if err != nil {
		return empty, err
	}
	response, err := service.deps.Do(request)
	if err != nil {
		return empty, fmt.Errorf("%w: %v", errPeersDownstreamUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return empty, fmt.Errorf("%w: local daemon answered %s", errPeersDownstreamUnavailable, response.Status)
	}
	var body peers.ListResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body); err != nil {
		return empty, fmt.Errorf("%w: decode local daemon response: %v", errPeersDownstreamUnavailable, err)
	}
	return body, nil
}

func (service Service) upstreamTrustChange(request TrustRequest) (TrustResult, bool, error) {
	upstream, found, err := wbconfig.LoadPeersUpstream(service.deps.ConfigPath())
	if err != nil {
		return TrustResult{}, true, err
	}
	if found {
		name := upstreamDisplayName(upstream.URL)
		if strings.EqualFold(request.Peer, "upstream") || strings.EqualFold(request.Peer, name) || strings.EqualFold(request.Peer, upstream.URL) {
			path, err := peerUpstreamStatePath(request.Root)
			if err != nil {
				return TrustResult{}, true, err
			}
			state, err := loadPeerUpstreamState(path)
			if err != nil {
				return TrustResult{}, true, err
			}
			state.Blocked = request.Action == Block
			if err := savePeerUpstreamState(path, state); err != nil {
				return TrustResult{}, true, err
			}
			trust := "active"
			if state.Blocked {
				trust = "blocked"
			}
			return TrustResult{Response: peers.TrustResponse{PeerID: name, Name: name, Trust: trust}, Upstream: true}, true, nil
		}
	}
	return TrustResult{}, false, nil
}
