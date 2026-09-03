package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

func newDepsPropagateCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "propagate",
		Short: "Build consumers against a library's working tree, or publish and bump at the end",
		Long: `Propagation has two halves.

'local' is the normal path inside a stream: a consumer builds against the
library's WORKING TREE through an untracked link, so a change is proven across
every affected repository before anything is published.

The remote half — publish, then bump exactly the repositories the stream linked
— is the end-of-stream wave and is not part of this command yet.`,
	}
	command.AddCommand(newDepsPropagateLocalCmd())
	setDiscoveryTerms(command, "propagate local remote link library consumer unpublished working tree stream")
	return command
}

type depsPropagateLocalOptions struct {
	consumers []string
	undo      bool
	verify    bool
	stream    string
	format    string
	timeout   time.Duration
}

func newDepsPropagateLocalCmd() *cobra.Command {
	options := depsPropagateLocalOptions{}
	command := &cobra.Command{
		Use:   "local [library-worktree]",
		Short: "Link consumers to a library's working tree without changing one tracked file",
		Long: `Build consumers against a library's WORKING TREE instead of a published version.

WB discovers what the library publishes from the library worktree itself — the
Go module path from backend/go.mod (or the module root), and npm package names
from libs/**/package.json. An operator-supplied package name is never accepted
as a substitute, and a consumer that declares none of the discovered identities
is reported and skipped rather than linked to something it does not use.

A Go consumer gets a Git-excluded go.work at its worktree root whose 'use'
entries name EVERY module in the consumer worktree plus the library's. go.mod is
never touched and no replace directive is added. CI is unaffected structurally:
the file does not exist in the repository, so a CI checkout has no go.work to
honour; GOWORK=off is the explicit guarantee where a toolchain might discover
one anyway.

An npm consumer first proves a clean frozen install of its unlinked tree, so a
link never masks a lockfile or manifest mismatch. The library is then built once
with the repository's own build target, cached against the library's CONTENT
HASH and rebuilt whenever that hash moves, and linked into node_modules. No
pnpm override, alias, or workspace: entry is ever written, and no tracked file
changes.

While a link is live, do NOT run 'go mod tidy' or 'go get': both resolve against
the workspace and would write a go.sum describing an unpublished library tree.
This verb never runs them.

Every link is recorded in stream state at the moment it is created, with enough
detail to reverse it exactly. --undo restores the published versions and removes
the link, and succeeds even when the library worktree has since been removed,
because the record — not the library — is the source of truth for reversal.

--verify runs each consumer's lint and tests against the linked copy through the
existing wb verify profiles, constrained to a single worker: Go with
'go test -p 1' and without -race, Node with '--parallel=1 --maxWorkers=1' and
NX_DAEMON=false, NX_SKIP_NX_CACHE=true. It reports per consumer and does not stop
at the first failure. Every run states
'verified against unpublished <library> at content-hash <h> (dirty)' and prints
the links in effect with the published version each replaced. It also runs a
GOWORK=off build and vet as the pre-landing check.

Links are recorded in stream state BEFORE the filesystem is touched, so a crash
mid-apply leaves a record --undo can act on. A consumer that no open stream
holds is refused (link-not-recordable) before anything is written: an unrecorded
link cannot be undone and is invisible to the merge guard.

Every verb that pushes, lands or absorbs work refuses a worktree with a live
link — merge, merge prepare, merge land, merge resume. There is no flag that
both bypasses that guard and pushes.`,
		Example: `# Link two consumers to a library worktree and verify them
wb deps propagate local /path/to/library --to /path/to/app --to /path/to/site --verify

# Restore published versions
wb deps propagate local --to /path/to/app --undo`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(options.format, "text", "json"); err != nil {
				return err
			}
			library := ""
			if len(args) == 1 {
				library = args[0]
			}
			if !options.undo && library == "" {
				return fmt.Errorf("a library worktree is required; pass its path, or use --undo to reverse recorded links")
			}
			if len(options.consumers) == 0 {
				return fmt.Errorf("at least one --to <consumer-worktree> is required")
			}
			store, err := streams.Open(projectsRoot)
			if err != nil {
				return err
			}
			home, err := wbhome.Root(projectsRoot)
			if err != nil {
				return err
			}
			engine := &locallink.Engine{
				Store:     store,
				Git:       locallink.ExecGit{Timeout: options.timeout},
				Verifier:  locallink.QualityVerifier{Options: quality.RunOptions{Timeout: options.timeout}},
				CacheRoot: filepath.Join(home, "cache", "local-link"),
			}
			// The Node port needs the library's content hash to key its build
			// cache, so it is built after discovery rather than before.
			engine.Node = locallink.ExecNode{CacheRoot: engine.CacheRoot, Timeout: options.timeout}
			if !options.undo {
				hash, _, hashErr := engine.Git.ContentHash(command.Context(), library)
				if hashErr != nil {
					return hashErr
				}
				engine.Node = locallink.ExecNode{CacheRoot: engine.CacheRoot, ContentHash: hash, Timeout: options.timeout}
			}
			result, err := engine.Run(command.Context(), locallink.Options{
				Library: library, Consumers: options.consumers, Undo: options.undo,
				Verify: options.verify, Timeout: options.timeout, Stream: options.stream,
			})
			if err != nil {
				// A guard that fired is exit 2 with the command that
				// satisfies it; a failure is exit 1.
				if refusal, refused := locallink.Refused(err); refused {
					return &exitError{code: exitUsage, message: refusal.Error()}
				}
				return err
			}
			if err := printPropagateLocal(command, options.format, result); err != nil {
				return err
			}
			if result.Failed() {
				return &exitError{code: exitFindings, message: "local propagation reported findings; see the report above"}
			}
			return nil
		},
	}
	command.Flags().StringArrayVar(&options.consumers, "to", nil, "consumer worktree to link or unlink (repeatable)")
	command.Flags().BoolVar(&options.undo, "undo", false, "restore the published versions the record names and remove the links")
	command.Flags().BoolVar(&options.verify, "verify", false, "run each consumer's lint and tests against the linked copy, single-worker")
	command.Flags().StringVar(&options.stream, "stream", "", "stream whose state records the links (default: the stream holding the library or the first consumer)")
	command.Flags().StringVar(&options.format, "format", "text", "stdout format: text or json")
	command.Flags().DurationVar(&options.timeout, "timeout", 30*time.Minute, "maximum duration per external command")
	setDiscoveryTerms(command, "propagate local link unlink undo go.work workspace pnpm dist content hash unpublished verify")
	return command
}

func printPropagateLocal(command *cobra.Command, format string, result locallink.Result) error {
	if format == "json" {
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	out := command.OutOrStdout()
	// The verb states which checks it will run before reporting what they
	// found, so a caller never has to infer the plan from the outcome.
	if len(result.Plan) > 0 {
		if _, err := fmt.Fprintln(out, "plan:"); err != nil {
			return err
		}
		for _, step := range result.Plan {
			if _, err := fmt.Fprintf(out, "  - %s\n", step); err != nil {
				return err
			}
		}
	}
	if result.ContentHash != "" {
		state := "clean"
		if result.Dirty {
			state = "dirty"
		}
		if _, err := fmt.Fprintf(out, "\nlibrary %s at content-hash %s (%s)\n", result.Library, result.ContentHash, state); err != nil {
			return err
		}
		for _, identity := range result.Identities {
			if _, err := fmt.Fprintf(out, "  publishes %s %s (%s)\n", identity.Ecosystem, identity.Name, identity.Manifest); err != nil {
				return err
			}
		}
	}
	for _, consumer := range result.Consumers {
		if _, err := fmt.Fprintf(out, "\n%s\n", consumer.Consumer); err != nil {
			return err
		}
		if consumer.Skipped {
			if _, err := fmt.Fprintf(out, "  skipped: %s\n", consumer.Reason); err != nil {
				return err
			}
			continue
		}
		for _, link := range consumer.Links {
			if _, err := fmt.Fprintf(out, "  linked %s via %s (was %s)\n", link.Identity, link.Mechanism, link.PreviousVersion); err != nil {
				return err
			}
		}
		for _, skipped := range consumer.SkippedChecks {
			if _, err := fmt.Fprintf(out, "  ? not checked: %s\n", skipped); err != nil {
				return err
			}
		}
		if consumer.Verification != nil {
			if _, err := fmt.Fprintf(out, "  %s\n", consumer.Verification.Statement); err != nil {
				return err
			}
			for _, active := range consumer.Verification.ActiveLinks {
				if _, err := fmt.Fprintf(out, "  active link: %s\n", active); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(out, "  linked run: passed=%t %s\n", consumer.Verification.Linked.Passed, consumer.Verification.Linked.Command); err != nil {
				return err
			}
			for _, detail := range consumer.Verification.Linked.Details {
				if _, err := fmt.Fprintf(out, "    ! %s\n", detail); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(out, "  published baseline: passed=%t %s\n", consumer.Verification.PublishedBaseline.Passed, consumer.Verification.PublishedBaseline.Command); err != nil {
				return err
			}
			for _, detail := range consumer.Verification.PublishedBaseline.Details {
				if _, err := fmt.Fprintf(out, "    ! %s\n", detail); err != nil {
					return err
				}
			}
		}
		for _, failure := range consumer.Errors {
			if _, err := fmt.Fprintf(out, "  ! %s\n", failure); err != nil {
				return err
			}
		}
	}
	if !result.Failed() && len(result.Consumers) > 0 && result.ContentHash != "" {
		if _, err := fmt.Fprintf(out, "\nwhile these links are live, do not run `go mod tidy` or `go get`: both resolve against the workspace.\nclear them with `wb deps propagate local --to %s --undo`.\n",
			strings.Join(consumerPaths(result), " --to ")); err != nil {
			return err
		}
	}
	return nil
}

func consumerPaths(result locallink.Result) []string {
	paths := make([]string, 0, len(result.Consumers))
	for _, consumer := range result.Consumers {
		if consumer.Skipped {
			continue
		}
		paths = append(paths, consumer.Consumer)
	}
	return paths
}
