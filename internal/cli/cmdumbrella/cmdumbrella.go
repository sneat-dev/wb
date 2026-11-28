// Package cmdumbrella implements the `wb umbrella` command tree (`show`, `close`).
package cmdumbrella

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/cli/cmdcatalog"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/umbrella"
	"github.com/spf13/cobra"
)

// Dependencies injects provider, config, and WB member lookup seams.
type Dependencies struct {
	DefaultRegistry func() (string, error)
	Providers       *umbrella.Registry
	Members         func(projectsRoot string, ref umbrella.Ref) ([]umbrella.Member, error)
}

func (d Dependencies) resolveDefaultRegistry() (string, error) {
	if d.DefaultRegistry != nil {
		return d.DefaultRegistry()
	}
	return umbrella.DefaultRegistry, nil
}

func (d Dependencies) resolveProviders() *umbrella.Registry {
	if d.Providers != nil {
		return d.Providers
	}
	return umbrella.DefaultProviders(nil)
}

// ShowDocument is the JSON envelope emitted by `wb umbrella show`.
type ShowDocument struct {
	Ref          string            `json:"ref"`
	Registry     string            `json:"registry"`
	ID           string            `json:"id"`
	Link         string            `json:"link"`
	State        umbrella.State    `json:"state"`
	Members      []umbrella.Member `json:"members"`
	ReadyToClose bool              `json:"ready_to_close"`
	CloseCommand string            `json:"close_command,omitempty"`
}

// CloseDocument is the JSON envelope emitted by `wb umbrella close`.
type CloseDocument struct {
	Ref          string            `json:"ref"`
	Registry     string            `json:"registry"`
	ID           string            `json:"id"`
	Link         string            `json:"link"`
	State        umbrella.State    `json:"state"`
	Applied      bool              `json:"applied"`
	Closed       bool              `json:"closed"`
	Refused      bool              `json:"refused,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	Summary      string            `json:"summary,omitempty"`
	Members      []umbrella.Member `json:"members"`
	Blocking     []umbrella.Member `json:"blocking,omitempty"`
	ApplyCommand string            `json:"apply_command,omitempty"`
}

// New constructs the `wb umbrella` parent command and its `show` and `close` verbs.
func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "umbrella",
		Short: "Inspect and close cross-repository umbrella index issues",
		Long: `Inspect and close umbrella references that index multi-repository tasks.

An umbrella is a typed reference "<registry>:<id>". The default registry is
GitHub ("github:owner/repo#N", or bare "owner/repo#N"). The default registry
can be configured per fleet (~/.config/wb/worktrees.yaml) or per repository
(.wb/worktrees.yaml) under umbrella.default_registry.

WB never closes an umbrella automatically. Use 'wb umbrella show <ref>' to
inspect the umbrella's state and every recorded member, and
'wb umbrella close <ref> --apply' once every member has landed or been
archived with a restore pointer.`,
	}
	command.AddCommand(
		newShow(runtime, deps),
		newClose(runtime, deps),
	)
	return command
}

func newShow(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:   "show <ref>",
		Short: "Show an umbrella's open/closed state and all recorded members",
		Long: `Show the open/closed state of an umbrella reference from its provider
together with every recorded member task, repository, pull request, and state
from WB's own Work Log and retirement records.

Private umbrella title and body prose are never read or echoed; WB displays
only the reference, open/closed state, and local member records.`,
		Example: `# Inspect a GitHub umbrella issue (bare owner/repo#N defaults to github)
wb umbrella show sneat-co/backstage#845

# Inspect using the explicit typed reference and JSON output
wb umbrella show github:sneat-co/backstage#845 --format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			ref, info, members, err := loadUmbrellaState(command.Context(), runtime, deps, args[0])
			if err != nil {
				return err
			}
			ready := allMembersReady(members)
			doc := ShowDocument{
				Ref:          info.Ref.String(),
				Registry:     info.Ref.Registry,
				ID:           info.Ref.ID,
				Link:         info.Link,
				State:        info.State,
				Members:      nonNilMembers(members),
				ReadyToClose: ready,
			}
			if info.State == umbrella.StateOpen && ready {
				doc.CloseCommand = umbrella.CloseCommand(ref)
			}
			if format == "json" {
				return encodeJSON(command.OutOrStdout(), doc)
			}
			return renderShowText(command.OutOrStdout(), doc)
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	cmdcatalog.SetDiscoveryTerms(command, "umbrella show inspect cross-repo index members status open closed")
	return command
}

func newClose(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var apply bool
	var format string
	command := &cobra.Command{
		Use:   "close <ref>",
		Short: "Verify all members finished, post a final summary, and close the umbrella",
		Long: `Plan or apply closing an umbrella reference.

Dry run is the default: without --apply, WB verifies that the umbrella is open
and that every recorded member has landed or been archived with a restore
pointer, then prints the closing summary and exact --apply command without
mutating the provider.

Closing is refused while any member is neither landed nor archived with a
restore pointer, naming every blocking member and its unmet condition. With
--apply, WB posts the final summary comment (members, landed SHAs, and archive
refs) and closes the umbrella through its provider. This is the only way WB
closes an umbrella, and it is never automatic.`,
		Example: `# Dry run: verify every member has landed or been archived
wb umbrella close sneat-co/backstage#845

# Post the final summary comment and close the umbrella
wb umbrella close sneat-co/backstage#845 --apply

# Machine-readable dry-run or apply output
wb umbrella close github:sneat-co/backstage#845 --format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			ref, info, members, err := loadUmbrellaState(command.Context(), runtime, deps, args[0])
			if err != nil {
				return err
			}
			doc := CloseDocument{
				Ref:      info.Ref.String(),
				Registry: info.Ref.Registry,
				ID:       info.Ref.ID,
				Link:     info.Link,
				State:    info.State,
				Applied:  apply,
				Members:  nonNilMembers(members),
			}
			if info.State == umbrella.StateClosed {
				doc.Refused = true
				doc.Reason = fmt.Sprintf("umbrella %s is already closed", info.Ref.String())
				return emitCloseRefusal(command, runtime, format, doc)
			}
			if len(members) == 0 {
				doc.Refused = true
				doc.Reason = fmt.Sprintf("cannot close umbrella %s: no recorded members in WB; attach tasks with `wb worktree set <task> --umbrella %s`",
					info.Ref.String(), ref.Display())
				return emitCloseRefusal(command, runtime, format, doc)
			}
			var blocking []umbrella.Member
			for _, member := range members {
				if !member.ReadyToClose {
					blocking = append(blocking, member)
				}
			}
			if len(blocking) > 0 {
				doc.Refused = true
				doc.Blocking = blocking
				doc.Reason = formatBlockingRefusal(info.Ref, blocking)
				return emitCloseRefusal(command, runtime, format, doc)
			}

			summary := umbrella.FormatCloseSummary(info.Ref, members)
			doc.Summary = summary
			if !apply {
				doc.ApplyCommand = umbrella.CloseCommand(ref)
				if format == "json" {
					return encodeJSON(command.OutOrStdout(), doc)
				}
				return renderCloseText(command.OutOrStdout(), doc)
			}

			if err := deps.resolveProviders().Close(command.Context(), info.Ref, summary); err != nil {
				return err
			}
			doc.Closed = true
			doc.State = umbrella.StateClosed
			if format == "json" {
				return encodeJSON(command.OutOrStdout(), doc)
			}
			return renderCloseText(command.OutOrStdout(), doc)
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "post the final summary comment and close the umbrella")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	cmdcatalog.SetDiscoveryTerms(command, "umbrella close finish cross-repo index summary dry-run apply")
	return command
}

func loadUmbrellaState(ctx context.Context, runtime shared.Runtime, deps Dependencies, raw string) (umbrella.Ref, umbrella.Info, []umbrella.Member, error) {
	defaultRegistry, err := deps.resolveDefaultRegistry()
	if err != nil {
		return umbrella.Ref{}, umbrella.Info{}, nil, err
	}
	ref, err := umbrella.Parse(raw, defaultRegistry)
	if err != nil {
		return umbrella.Ref{}, umbrella.Info{}, nil, runtime.ExitError(shared.ExitUsage, err.Error())
	}
	providers := deps.resolveProviders()
	if _, provErr := providers.Provider(ref.Registry); provErr != nil {
		return umbrella.Ref{}, umbrella.Info{}, nil, runtime.ExitError(shared.ExitUsage, provErr.Error())
	}
	info, err := providers.Resolve(ctx, ref)
	if err != nil {
		return umbrella.Ref{}, umbrella.Info{}, nil, err
	}
	members, err := deps.Members(runtime.Flags().ProjectsRoot, info.Ref)
	if err != nil {
		return umbrella.Ref{}, umbrella.Info{}, nil, err
	}
	return info.Ref, info, members, nil
}

func allMembersReady(members []umbrella.Member) bool {
	if len(members) == 0 {
		return false
	}
	for _, member := range members {
		if !member.ReadyToClose {
			return false
		}
	}
	return true
}

func nonNilMembers(members []umbrella.Member) []umbrella.Member {
	if members == nil {
		return []umbrella.Member{}
	}
	return members
}

func formatBlockingRefusal(ref umbrella.Ref, blocking []umbrella.Member) string {
	parts := make([]string, 0, len(blocking))
	for _, member := range blocking {
		reason := member.BlockingReason
		if reason == "" {
			reason = "neither landed nor archived with a restore pointer"
		}
		parts = append(parts, fmt.Sprintf("%s (%s): %s — %s", member.Task, member.Repository, member.State, reason))
	}
	return fmt.Sprintf("cannot close umbrella %s: %d member(s) neither landed nor archived with a restore pointer: %s",
		ref.String(), len(blocking), strings.Join(parts, "; "))
}

func emitCloseRefusal(command *cobra.Command, runtime shared.Runtime, format string, doc CloseDocument) error {
	if format == "json" {
		if err := encodeJSON(command.OutOrStdout(), doc); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(command.OutOrStdout(), "refused: %s\n", doc.Reason); err != nil {
			return err
		}
	}
	return runtime.ExitError(shared.ExitUsage, doc.Reason)
}

func encodeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func renderShowText(out io.Writer, doc ShowDocument) error {
	if _, err := fmt.Fprintf(out, "umbrella: %s (%s)\nstate:    %s\nmembers:  %d\n", doc.Ref, doc.Link, doc.State, len(doc.Members)); err != nil {
		return err
	}
	if len(doc.Members) == 0 {
		_, err := fmt.Fprintln(out, "no recorded members")
		return err
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	for _, member := range doc.Members {
		if err := writeMemberLine(out, member); err != nil {
			return err
		}
	}
	if doc.CloseCommand != "" {
		if _, err := fmt.Fprintf(out, "\nnext: %s\n", doc.CloseCommand); err != nil {
			return err
		}
	}
	return nil
}

func renderCloseText(out io.Writer, doc CloseDocument) error {
	if doc.Closed {
		if _, err := fmt.Fprintf(out, "closed umbrella %s (%s)\n", doc.Ref, doc.Link); err != nil {
			return err
		}
		return nil
	}
	if _, err := fmt.Fprintf(out, "dry-run: umbrella %s (%s) is ready to close (%d member(s))\n\n", doc.Ref, doc.Link, len(doc.Members)); err != nil {
		return err
	}
	for _, member := range doc.Members {
		if err := writeMemberLine(out, member); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "\napply with: %s\n", doc.ApplyCommand); err != nil {
		return err
	}
	return nil
}

func writeMemberLine(out io.Writer, member umbrella.Member) error {
	pr := "-"
	if member.PullRequest > 0 {
		pr = fmt.Sprintf("#%d", member.PullRequest)
	}
	ref := "-"
	switch {
	case member.LandedSHA != "":
		ref = member.LandedSHA
		if len(ref) > 12 {
			ref = ref[:12]
		}
	case member.RestorePointer != "":
		ref = member.RestorePointer
	case member.ArchiveRef != "":
		ref = member.ArchiveRef
	}
	_, err := fmt.Fprintf(out, "- %s  %s  pr=%s  state=%s  ref=%s\n", member.Task, member.Repository, pr, member.State, ref)
	return err
}
