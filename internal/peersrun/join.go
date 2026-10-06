package peersrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/credentialfile"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

func (service Service) Join(ctx context.Context, request JoinRequest, in io.Reader, warn func(error)) (JoinOutput, error) {
	deps := service.join
	hubURL := strings.TrimRight(strings.TrimSpace(request.HubURL), "/")
	if err := remotestate.ValidateHubURL(hubURL); err != nil {
		return JoinOutput{}, refusal(Usage, err.Error())
	}
	switch {
	case request.TokenStdin && strings.TrimSpace(request.TokenFile) != "":
		return JoinOutput{}, refusal(Usage, "pass either --token-stdin or --token-file, not both")
	case !request.TokenStdin && strings.TrimSpace(request.TokenFile) == "":
		return JoinOutput{}, refusal(Usage, "--token-stdin or --token-file is required")
	}
	token, err := readPeersJoinToken(in, request.TokenFile, request.TokenStdin)
	if err != nil {
		return JoinOutput{}, refusal(Usage, err.Error())
	}
	configPath := deps.ConfigPath()
	var unconfigured *remotestate.UnconfiguredError
	switch cfg, loadErr := remotestate.LoadConfig(configPath); {
	case loadErr == nil && cfg.Provider == "hub" && remotestate.SameOrigin(cfg.URL, hubURL):
		return JoinOutput{}, refusal(Usage, fmt.Sprintf("remote.provider is already hub at %s; two receivers cannot consume one queue", cfg.URL))
	case loadErr != nil && !errors.As(loadErr, &unconfigured):
		return JoinOutput{}, fmt.Errorf("load %s: %w", configPath, loadErr)
	}
	if err := deps.Verify(ctx, hubURL, token); err != nil {
		return JoinOutput{}, refusal(Findings, "verify peer token: "+err.Error())
	}
	if err := deps.EnsureNodeIdentity(request.Root); err != nil {
		warn(err)
	}
	digest := sha256.Sum256([]byte(token))
	filename := fmt.Sprintf("peer-%s-%s.token", hostForFilename(hubURL), hex.EncodeToString(digest[:3]))
	path := filepath.Join(filepath.Dir(configPath), "credentials", filename)
	created, err := credentialfile.WritePrivate(path, token)
	if err != nil {
		return JoinOutput{}, err
	}
	if err := wbconfig.SetPeersUpstream(configPath, hubURL, path); err != nil {
		if created {
			_ = os.Remove(path)
		}
		return JoinOutput{}, err
	}
	restarted := false
	if request.RestartDaemon {
		if err := deps.Restart(ctx, request.Root); err != nil {
			return JoinOutput{}, fmt.Errorf("peer join saved, but daemon restart failed: %w", err)
		}
		restarted = true
	}
	return JoinOutput{HubURL: hubURL, ConfigPath: configPath, TokenFile: path, Verified: true, DaemonRestart: restarted}, nil
}
