package cmdworktree

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/umbrella"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func defaultResolveUmbrella(ctx context.Context, projectsRoot string, repositories []string, raw string) (umbrella.Ref, error) {
	return resolveUmbrellaWithProviders(ctx, projectsRoot, repositories, raw, nil)
}

func resolveUmbrellaWithProviders(ctx context.Context, projectsRoot string, repositories []string, raw string, providers *umbrella.Registry) (umbrella.Ref, error) {
	defaultRegistry := ""
	if !umbrella.HasExplicitRegistry(raw) {
		var err error
		defaultRegistry, err = worktrees.ResolveUmbrellaDefaultRegistryForSlugs(projectsRoot, repositories)
		if err != nil {
			return umbrella.Ref{}, err
		}
	}
	ref, err := umbrella.Parse(raw, defaultRegistry)
	if err != nil {
		return umbrella.Ref{}, err
	}
	reg := providers
	if reg == nil {
		reg = umbrella.DefaultProviders(nil)
	}
	info, err := reg.Resolve(ctx, ref)
	if err != nil {
		return umbrella.Ref{}, err
	}
	return info.Ref, nil
}

func postUmbrellaStatusAndOfferClose(ctx context.Context, command *cobra.Command, projectsRoot string, providers *umbrella.Registry, format string, update umbrella.StatusUpdate, rawUmbrella string) {
	rawUmbrella = strings.TrimSpace(rawUmbrella)
	if rawUmbrella == "" {
		rawUmbrella = worktrees.UmbrellaForTask(projectsRoot, update.Task)
	}
	if rawUmbrella == "" {
		return
	}
	ref, err := umbrella.Parse(rawUmbrella, "")
	if err != nil {
		return
	}
	reg := providers
	if reg == nil {
		reg = umbrella.DefaultProviders(nil)
	}
	line := umbrella.FormatStatusLine(update)
	if err := reg.AppendStatus(ctx, ref, line); err != nil {
		_, _ = fmt.Fprintf(command.ErrOrStderr(), "warning: failed to post status to umbrella %s: %v\n", ref.String(), err)
	}
	if format != "json" {
		if ready, _, readyErr := worktrees.UmbrellaReadyToClose(projectsRoot, ref); readyErr == nil && ready {
			_, _ = fmt.Fprintf(command.OutOrStdout(), "all umbrella members finished; close with: %s\n", umbrella.CloseCommand(ref))
		}
	}
}

func writeUmbrellaCloseOffer(out io.Writer, projectsRoot, rawUmbrella string) error {
	rawUmbrella = strings.TrimSpace(rawUmbrella)
	if rawUmbrella == "" {
		return nil
	}
	ref, err := umbrella.Parse(rawUmbrella, "")
	if err != nil {
		return nil
	}
	ready, _, err := worktrees.UmbrellaReadyToClose(projectsRoot, ref)
	if err != nil || !ready {
		return nil
	}
	_, err = fmt.Fprintf(out, "\nall umbrella members finished; close with: %s\n", umbrella.CloseCommand(ref))
	return err
}
